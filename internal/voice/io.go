package voice

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// Transcriber produces one utterance per call. It is the speech-to-text
// boundary: [TextTranscriber] types it, [HTTPTranscriber] records audio and
// asks a transcription service.
//
// Listen returns io.EOF when the input has ended, which the loop treats as a
// clean exit rather than an error.
type Transcriber interface {
	Listen(ctx context.Context) (string, error)
}

// Speaker delivers one of qi's replies. It is the text-to-speech boundary.
type Speaker interface {
	Speak(ctx context.Context, text string) error
}

// Runner executes an external command. Injecting it keeps the audio tools
// (say, ffmpeg) out of the test path: no test in this package spawns a
// process.
type Runner func(ctx context.Context, name string, args ...string) error

// ExecRunner runs the command with os/exec, discarding its output. Arguments
// are passed as separate argv elements and never through a shell, so a
// spoken phrase can never become a command.
func ExecRunner(ctx context.Context, name string, args ...string) error {
	return exec.CommandContext(ctx, name, args...).Run()
}

// TextTranscriber reads one utterance per line from an io.Reader. It is the
// reference input path (`qi voice --text`) and what the tests drive: unlike
// live speech-to-text, it is exactly reproducible.
type TextTranscriber struct {
	sc     *bufio.Scanner
	w      io.Writer
	Prompt string // written before each read when w is non-nil; default "You: "
}

// NewTextTranscriber reads utterances from r. When w is non-nil it writes a
// prompt before each read, so an interactive terminal shows a turn marker.
func NewTextTranscriber(r io.Reader, w io.Writer) *TextTranscriber {
	return &TextTranscriber{sc: bufio.NewScanner(r), w: w, Prompt: "You: "}
}

// Listen returns the next non-blank line, or io.EOF at end of input.
//
// Blank lines are skipped rather than reported as an unrecognised utterance:
// pressing enter is not a question.
//
// ctx is honoured between lines only. A blocking read on a terminal cannot be
// interrupted, so a cancel during a read takes effect after the user presses
// enter.
func (t *TextTranscriber) Listen(ctx context.Context) (string, error) {
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if t.w != nil && t.Prompt != "" {
			if _, err := io.WriteString(t.w, t.Prompt); err != nil {
				return "", err
			}
		}
		if !t.sc.Scan() {
			if err := t.sc.Err(); err != nil {
				return "", err
			}
			return "", io.EOF
		}
		if line := strings.TrimSpace(t.sc.Text()); line != "" {
			return line, nil
		}
	}
}

// EchoSpeaker writes replies as text, prefixed so the transcript reads as a
// conversation.
type EchoSpeaker struct {
	w      io.Writer
	Prefix string // default "Qi: "
}

// NewEchoSpeaker writes "Qi: <text>" lines to w.
func NewEchoSpeaker(w io.Writer) *EchoSpeaker {
	return &EchoSpeaker{w: w, Prefix: "Qi: "}
}

// Speak writes one reply line.
func (s *EchoSpeaker) Speak(ctx context.Context, text string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.w == nil {
		return nil
	}
	_, err := fmt.Fprintf(s.w, "%s%s\n", s.Prefix, text)
	return err
}

// SaySpeaker speaks through an external text-to-speech binary, macOS `say` by
// default.
//
// The text is passed as a single argv element via the injected [Runner], never
// through a shell: a reply may quote an agent's terminal output, which can
// contain anything at all.
//
// Whether this speaker is appropriate is the command layer's decision (it
// knows runtime.GOOS and the user's flags); this package only knows how to
// drive it.
type SaySpeaker struct {
	Bin  string // default "say"
	Args []string
	run  Runner
	w    io.Writer
}

// NewSaySpeaker builds a speaker over run. When w is non-nil the text is also
// echoed there, so the transcript stays readable with audio on.
func NewSaySpeaker(w io.Writer, run Runner) *SaySpeaker {
	return &SaySpeaker{Bin: "say", run: run, w: w}
}

// Speak echoes the text (when configured) and hands it to the binary.
func (s *SaySpeaker) Speak(ctx context.Context, text string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.w != nil {
		if _, err := fmt.Fprintf(s.w, "Qi: %s\n", text); err != nil {
			return err
		}
	}
	if s.run == nil {
		return errors.New("voice: SaySpeaker has no runner")
	}
	bin := s.Bin
	if bin == "" {
		bin = "say"
	}
	args := append(append([]string(nil), s.Args...), text)
	return s.run(ctx, bin, args...)
}

// MultiSpeaker fans one reply out to several speakers (echo to the terminal
// and speak aloud). Every speaker is called even if an earlier one fails, so a
// broken audio device never silences the transcript.
type MultiSpeaker struct {
	Speakers []Speaker
}

// NewMultiSpeaker fans out to speakers in order.
func NewMultiSpeaker(speakers ...Speaker) *MultiSpeaker {
	return &MultiSpeaker{Speakers: speakers}
}

// Speak delivers text to every speaker, joining any errors.
func (m *MultiSpeaker) Speak(ctx context.Context, text string) error {
	var errs []error
	for _, s := range m.Speakers {
		if s == nil {
			continue
		}
		if err := s.Speak(ctx, text); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
