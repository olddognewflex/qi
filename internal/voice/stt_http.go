package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// HTTPTranscriber records a clip and transcribes it through an
// OpenAI-compatible /v1/audio/transcriptions endpoint (OpenAI Whisper, a
// local whisper.cpp server, ...).
//
// Live speech-to-text is UNVERIFIED IN CI: nothing here is exercised against a
// real microphone or a real service, only against httptest and a fake command
// runner. `qi voice --text` (see [TextTranscriber]) is the reference input
// path; treat this one as best-effort until it has been used by hand.
//
// The audio never touches qi's vault and the transcript is not persisted by
// this type. Note that speaking to a hosted endpoint sends the recording off
// the machine, which is the user's opt-in to make at the command layer.
type HTTPTranscriber struct {
	// URL is the full endpoint, e.g.
	// "https://api.openai.com/v1/audio/transcriptions".
	URL string
	// Model is the transcription model name, sent as the "model" field.
	Model string
	// APIKey, when set, is sent as a bearer token.
	APIKey string
	// Client is the HTTP client; nil means http.DefaultClient.
	Client *http.Client
	// Record captures one utterance to a WAV file and returns its path. The
	// file is deleted after the request. See [FFmpegRecorder].
	Record func(ctx context.Context) (wavPath string, err error)
	// Prompt, when non-nil, receives a turn cue: recording is time-boxed and
	// silent, so without one the user has no idea when to start speaking.
	Prompt io.Writer
	// Hint, when set, is sent as the endpoint's "prompt" field: a short
	// vocabulary list ("qi, Herdr, Codex, Claude, workspace, pane") that
	// biases Whisper toward the names qi's grammar needs to recognise.
	Hint string
}

// transcriptionResponse is the response_format=json shape.
type transcriptionResponse struct {
	Text string `json:"text"`
}

// Listen records one clip, uploads it, and returns the transcript.
func (t *HTTPTranscriber) Listen(ctx context.Context) (string, error) {
	if t.Record == nil {
		return "", fmt.Errorf("voice: HTTPTranscriber has no recorder")
	}
	if t.Prompt != nil {
		fmt.Fprint(t.Prompt, "Listening... ")
	}
	path, err := t.Record(ctx)
	if err != nil {
		return "", fmt.Errorf("record audio: %w", err)
	}
	if path != "" {
		defer os.Remove(path)
	}
	if t.Prompt != nil {
		fmt.Fprintln(t.Prompt, "transcribing...")
	}

	body, contentType, err := t.buildRequest(path)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.URL, body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", contentType)
	if t.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+t.APIKey)
	}

	client := t.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("transcribe: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("transcribe: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("transcribe: %s: %s", resp.Status, strings.TrimSpace(truncate(string(raw), 200)))
	}
	var out transcriptionResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("transcribe: decode response: %w", err)
	}
	text := strings.TrimSpace(out.Text)
	if t.Prompt != nil {
		// Show what was heard: without this a mis-transcription is
		// indistinguishable from a grammar gap.
		if text == "" {
			fmt.Fprintln(t.Prompt, "You: (nothing heard)")
		} else {
			fmt.Fprintf(t.Prompt, "You: %s\n", text)
		}
	}
	return text, nil
}

// buildRequest assembles the multipart body: the audio file, the model, and
// response_format=json.
func (t *HTTPTranscriber) buildRequest(path string) (io.Reader, string, error) {
	audio, err := os.Open(path)
	if err != nil {
		return nil, "", fmt.Errorf("open recording: %w", err)
	}
	defer audio.Close()

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile("file", filepath.Base(path))
	if err != nil {
		return nil, "", err
	}
	if _, err := io.Copy(part, audio); err != nil {
		return nil, "", fmt.Errorf("read recording: %w", err)
	}
	if t.Model != "" {
		if err := w.WriteField("model", t.Model); err != nil {
			return nil, "", err
		}
	}
	if t.Hint != "" {
		if err := w.WriteField("prompt", t.Hint); err != nil {
			return nil, "", err
		}
	}
	if err := w.WriteField("response_format", "json"); err != nil {
		return nil, "", err
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return &buf, w.FormDataContentType(), nil
}

// recorderGOOS is runtime.GOOS, overridable in tests so the argv for a
// platform can be asserted from any host.
var recorderGOOS = runtime.GOOS

// FFmpegRecorder returns a recorder that captures seconds of mono 16 kHz audio
// to a temporary WAV file by shelling out to ffmpeg through run.
//
// Only macOS is supported: capture goes through avfoundation, where device is
// the numeric audio input index that `ffmpeg -f avfoundation -list_devices
// true -i ""` prints (":0" is usually the default microphone). Linux capture
// (pulse/alsa) is out of scope and returns an error rather than guessing at a
// device layer.
//
// Fixed-length recording is deliberate: it needs no voice-activity detection
// and no audio library, which keeps this package on the standard library.
func FFmpegRecorder(bin string, device string, seconds int, run Runner) func(ctx context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		f, err := os.CreateTemp("", "qi-voice-*.wav")
		if err != nil {
			return "", err
		}
		out := f.Name()
		if err := f.Close(); err != nil {
			os.Remove(out)
			return "", err
		}
		args, err := ffmpegArgs(recorderGOOS, device, seconds, out)
		if err != nil {
			os.Remove(out)
			return "", err
		}
		if bin == "" {
			bin = "ffmpeg"
		}
		if run == nil {
			os.Remove(out)
			return "", fmt.Errorf("voice: FFmpegRecorder has no runner")
		}
		if err := run(ctx, bin, args...); err != nil {
			os.Remove(out)
			return "", fmt.Errorf("ffmpeg: %w", err)
		}
		return out, nil
	}
}

// ffmpegArgs builds the capture argv for goos, or reports that the platform is
// unsupported. It is pure so the command line can be asserted in a test.
func ffmpegArgs(goos, device string, seconds int, out string) ([]string, error) {
	if goos != "darwin" {
		return nil, fmt.Errorf("voice: recording not supported on %s", goos)
	}
	if seconds <= 0 {
		seconds = 5
	}
	if device == "" {
		device = "0"
	}
	return []string{
		"-y",
		"-f", "avfoundation",
		"-i", ":" + device,
		"-t", strconv.Itoa(seconds),
		"-ac", "1",
		"-ar", "16000",
		out,
	}, nil
}

// truncate shortens s for an error message.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// STTHint builds the vocabulary hint sent as the transcription endpoint's
// "prompt". Whisper treats that field as preceding transcript, so a
// sentence-shaped hint ("Tell Claude in the qi workspace ...") gets copied
// onto near-matching audio — "kitchen workspace" came back as "qi
// workspace" in a live session. A bare list of proper nouns primes the
// spelling of names without offering a sentence to echo. Labels are the
// live workspace labels; extra is the user's own additions from config.
func STTHint(kinds []string, labels []string, extra string) string {
	var parts []string
	if len(kinds) > 0 {
		// The verbs go in as a list too: on short clips Whisper turned
		// "Tell" into "Tel", "Tele", and "Tail-".
		parts = append(parts, "Commands: Tell, Ask, Have, Quit.")
		parts = append(parts, "Agents: "+strings.Join(kinds, ", ")+".")
	}
	if len(labels) > 0 {
		parts = append(parts, "Workspaces: "+strings.Join(labels, ", ")+".")
	}
	if e := strings.TrimSpace(extra); e != "" {
		parts = append(parts, e)
	}
	return strings.Join(parts, " ")
}
