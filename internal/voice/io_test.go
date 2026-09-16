package voice

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestTextTranscriberReadsLines(t *testing.T) {
	in := strings.NewReader("what are my agents doing\n\n   \nquit\n")
	var out bytes.Buffer
	tr := NewTextTranscriber(in, &out)
	ctx := context.Background()

	got, err := tr.Listen(ctx)
	if err != nil || got != "what are my agents doing" {
		t.Fatalf("Listen() = %q, %v", got, err)
	}
	// Blank lines are skipped rather than reported as an utterance.
	got, err = tr.Listen(ctx)
	if err != nil || got != "quit" {
		t.Fatalf("Listen() = %q, %v; want the next non-blank line", got, err)
	}
	if _, err := tr.Listen(ctx); !errors.Is(err, io.EOF) {
		t.Fatalf("Listen() at end = %v, want io.EOF", err)
	}
	if n := strings.Count(out.String(), "You: "); n == 0 {
		t.Error("no prompt was written")
	}
}

func TestTextTranscriberHonoursCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tr := NewTextTranscriber(strings.NewReader("hello\n"), nil)
	if _, err := tr.Listen(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Listen() = %v, want context.Canceled", err)
	}
}

func TestEchoSpeaker(t *testing.T) {
	var out bytes.Buffer
	s := NewEchoSpeaker(&out)
	if err := s.Speak(context.Background(), "Codex finished."); err != nil {
		t.Fatalf("Speak: %v", err)
	}
	if got, want := out.String(), "Qi: Codex finished.\n"; got != want {
		t.Errorf("Speak wrote %q, want %q", got, want)
	}
}

func TestSaySpeakerPassesTextAsOneArgument(t *testing.T) {
	var gotName string
	var gotArgs []string
	var echo bytes.Buffer
	s := NewSaySpeaker(&echo, func(ctx context.Context, name string, args ...string) error {
		gotName, gotArgs = name, args
		return nil
	})

	// A reply can quote an agent's terminal output, so the text must survive
	// as a single argv element and never reach a shell.
	text := `Codex finished. rm -rf / && echo "done"`
	if err := s.Speak(context.Background(), text); err != nil {
		t.Fatalf("Speak: %v", err)
	}
	if gotName != "say" {
		t.Errorf("binary = %q, want %q", gotName, "say")
	}
	if len(gotArgs) != 1 || gotArgs[0] != text {
		t.Errorf("args = %q, want exactly one element %q", gotArgs, text)
	}
	if !strings.Contains(echo.String(), text) {
		t.Errorf("transcript = %q, want it to echo the reply", echo.String())
	}
}

func TestSaySpeakerWithoutRunner(t *testing.T) {
	s := &SaySpeaker{}
	if err := s.Speak(context.Background(), "hello"); err == nil {
		t.Fatal("want an error when no runner is configured")
	}
}

type failSpeaker struct{ err error }

func (f failSpeaker) Speak(context.Context, string) error { return f.err }

func TestMultiSpeakerFansOutDespiteFailure(t *testing.T) {
	var a, b bytes.Buffer
	boom := errors.New("audio device busy")
	m := NewMultiSpeaker(failSpeaker{boom}, NewEchoSpeaker(&a), nil, NewEchoSpeaker(&b))

	err := m.Speak(context.Background(), "hello")
	if !errors.Is(err, boom) {
		t.Fatalf("Speak() = %v, want the failing speaker's error", err)
	}
	if a.Len() == 0 || b.Len() == 0 {
		t.Error("a failing speaker must not silence the others")
	}
}
