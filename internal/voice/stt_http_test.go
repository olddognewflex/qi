package voice

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// stubRecorder writes audio to a temp WAV and returns its path.
func stubRecorder(t *testing.T, audio string) (func(ctx context.Context) (string, error), *string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "clip.wav")
	if err := os.WriteFile(path, []byte(audio), 0o600); err != nil {
		t.Fatal(err)
	}
	return func(context.Context) (string, error) { return path, nil }, &path
}

func TestHTTPTranscriberListen(t *testing.T) {
	record, path := stubRecorder(t, "RIFFfake-audio")

	var (
		gotAuth     string
		gotModel    string
		gotFormat   string
		gotFilename string
		gotAudio    string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		gotAuth = r.Header.Get("Authorization")
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("ParseMultipartForm: %v", err)
		}
		gotModel = r.FormValue("model")
		gotFormat = r.FormValue("response_format")
		f, hdr, err := r.FormFile("file")
		if err != nil {
			t.Fatalf("FormFile: %v", err)
		}
		defer f.Close()
		gotFilename = hdr.Filename
		b, _ := io.ReadAll(f)
		gotAudio = string(b)
		fmt.Fprint(w, `{"text":"  Tell Codex to run the tests.  "}`)
	}))
	defer srv.Close()

	var cue bytes.Buffer
	tr := &HTTPTranscriber{URL: srv.URL, Model: "whisper-1", APIKey: "sk-test", Client: srv.Client(), Record: record, Prompt: &cue}
	got, err := tr.Listen(context.Background())
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	if want := "Tell Codex to run the tests."; got != want {
		t.Errorf("Listen() = %q, want %q", got, want)
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotModel != "whisper-1" {
		t.Errorf("model = %q", gotModel)
	}
	if gotFormat != "json" {
		t.Errorf("response_format = %q", gotFormat)
	}
	if gotFilename != "clip.wav" {
		t.Errorf("filename = %q", gotFilename)
	}
	if gotAudio != "RIFFfake-audio" {
		t.Errorf("uploaded audio = %q", gotAudio)
	}
	// The recording is transient: it must not outlive the request.
	if _, err := os.Stat(*path); !os.IsNotExist(err) {
		t.Errorf("recording %s survived the call (stat err %v)", *path, err)
	}
	// Recording is silent and time-boxed, so the user needs a turn cue.
	if !strings.Contains(cue.String(), "Listening") {
		t.Errorf("turn cue = %q, want a listening prompt", cue.String())
	}
}

func TestHTTPTranscriberNon200(t *testing.T) {
	record, _ := stubRecorder(t, "RIFF")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model not found", http.StatusNotFound)
	}))
	defer srv.Close()

	tr := &HTTPTranscriber{URL: srv.URL, Client: srv.Client(), Record: record}
	_, err := tr.Listen(context.Background())
	if err == nil {
		t.Fatal("want an error for a non-200 response")
	}
	if !strings.Contains(err.Error(), "model not found") {
		t.Errorf("error = %q, want it to carry the response body", err)
	}
}

func TestHTTPTranscriberBadJSON(t *testing.T) {
	record, _ := stubRecorder(t, "RIFF")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "not json")
	}))
	defer srv.Close()

	tr := &HTTPTranscriber{URL: srv.URL, Client: srv.Client(), Record: record}
	if _, err := tr.Listen(context.Background()); err == nil {
		t.Fatal("want an error for an undecodable response")
	}
}

func TestHTTPTranscriberRecordFailure(t *testing.T) {
	tr := &HTTPTranscriber{URL: "http://example.invalid", Record: func(context.Context) (string, error) {
		return "", fmt.Errorf("no microphone")
	}}
	if _, err := tr.Listen(context.Background()); err == nil || !strings.Contains(err.Error(), "no microphone") {
		t.Fatalf("Listen() = %v, want the recorder's error", err)
	}
}

func TestHTTPTranscriberWithoutRecorder(t *testing.T) {
	tr := &HTTPTranscriber{URL: "http://example.invalid"}
	if _, err := tr.Listen(context.Background()); err == nil {
		t.Fatal("want an error when no recorder is configured")
	}
}

func TestFFmpegArgs(t *testing.T) {
	got, err := ffmpegArgs("darwin", "1", 4, "/tmp/out.wav")
	if err != nil {
		t.Fatalf("ffmpegArgs: %v", err)
	}
	want := []string{"-y", "-f", "avfoundation", "-i", ":1", "-t", "4", "-ac", "1", "-ar", "16000", "/tmp/out.wav"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ffmpegArgs() = %q, want %q", got, want)
	}

	got, err = ffmpegArgs("darwin", "", 0, "/tmp/out.wav")
	if err != nil {
		t.Fatalf("ffmpegArgs: %v", err)
	}
	if got[4] != ":0" || got[6] != "5" {
		t.Errorf("defaults = %q, want device :0 and 5 seconds", got)
	}

	if _, err := ffmpegArgs("linux", "0", 5, "/tmp/out.wav"); err == nil {
		t.Error("want an error on a platform with no supported capture path")
	}
}

func TestFFmpegRecorder(t *testing.T) {
	orig := recorderGOOS
	recorderGOOS = "darwin"
	defer func() { recorderGOOS = orig }()

	var gotName string
	var gotArgs []string
	rec := FFmpegRecorder("", "0", 3, func(ctx context.Context, name string, args ...string) error {
		gotName, gotArgs = name, args
		return nil
	})
	path, err := rec(context.Background())
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	defer os.Remove(path)

	if gotName != "ffmpeg" {
		t.Errorf("binary = %q, want ffmpeg", gotName)
	}
	if len(gotArgs) == 0 || gotArgs[len(gotArgs)-1] != path {
		t.Errorf("args = %q, want the output path %q last", gotArgs, path)
	}
	if filepath.Ext(path) != ".wav" {
		t.Errorf("recording path = %q, want a .wav", path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("recording file missing: %v", err)
	}
}

func TestFFmpegRecorderCleansUpOnFailure(t *testing.T) {
	orig := recorderGOOS
	recorderGOOS = "darwin"
	defer func() { recorderGOOS = orig }()

	var created string
	rec := FFmpegRecorder("ffmpeg", "0", 3, func(ctx context.Context, name string, args ...string) error {
		created = args[len(args)-1]
		return fmt.Errorf("device busy")
	})
	if _, err := rec(context.Background()); err == nil {
		t.Fatal("want the runner's error")
	}
	if created == "" {
		t.Fatal("runner was not called")
	}
	if _, err := os.Stat(created); !os.IsNotExist(err) {
		t.Errorf("temp recording %s was left behind", created)
	}
}

func TestFFmpegRecorderUnsupportedPlatform(t *testing.T) {
	orig := recorderGOOS
	recorderGOOS = "linux"
	defer func() { recorderGOOS = orig }()

	rec := FFmpegRecorder("ffmpeg", "0", 3, func(context.Context, string, ...string) error {
		t.Error("the runner must not be called on an unsupported platform")
		return nil
	})
	if _, err := rec(context.Background()); err == nil || !strings.Contains(err.Error(), "linux") {
		t.Fatalf("record() = %v, want an unsupported-platform error naming the GOOS", err)
	}
}

func TestHTTPTranscriber_SendsHintAsPrompt(t *testing.T) {
	var gotPrompt string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPrompt = r.FormValue("prompt")
		_, _ = w.Write([]byte(`{"text":"ok"}`))
	}))
	defer srv.Close()
	wav := filepath.Join(t.TempDir(), "clip.wav")
	if err := os.WriteFile(wav, []byte("RIFF"), 0o600); err != nil {
		t.Fatal(err)
	}
	tr := &HTTPTranscriber{URL: srv.URL, Model: "m", Hint: "qi, Codex", Record: func(context.Context) (string, error) { return wav, nil }}
	if _, err := tr.Listen(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gotPrompt != "qi, Codex" {
		t.Errorf("prompt = %q", gotPrompt)
	}
}

func TestSTTHint_IsANounListNotASentence(t *testing.T) {
	got := STTHint([]string{"Claude", "Codex"}, []string{"qi", "ai-map"}, " Herdr, pane ")
	want := "Agents: Claude, Codex. Workspaces: qi, ai-map. Herdr, pane"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
	if STTHint(nil, nil, "") != "" {
		t.Error("empty inputs should give an empty hint")
	}
}
