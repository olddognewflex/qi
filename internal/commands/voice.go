package commands

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"qi/internal/config"
	"qi/internal/service"
	"qi/internal/voice"
)

// newVoiceCommand is `qi voice`: the voice vertical slice. One utterance at
// a time is transcribed, parsed by the deterministic intent grammar, resolved
// to exactly one agent instance through the agent runtime (asking when
// ambiguous), sent, observed, and answered aloud. `--text` (or [voice]
// stt = "text", the default) types utterances instead of speaking them —
// the reference path that tests drive. `--once` handles a single utterance
// and exits, for scripting and smoke checks.
func newVoiceCommand(cfg config.Config) *cobra.Command {
	var (
		textMode bool
		once     string
		quiet    bool
		dryRun   bool
	)
	cmd := &cobra.Command{
		Use:   "voice",
		Short: "Talk to your coding agents: status, instructions, results (opt-in speech I/O)",
		Long: "A conversational loop over the agent runtime (Herdr when detected).\n" +
			"Say what your agents are doing, tell a specific agent to do something,\n" +
			"and hear the result. Agent instances are resolved explicitly — by kind,\n" +
			"workspace, focus, or the conversation so far — and qi asks when more\n" +
			"than one matches. Intent parsing is deterministic (no LLM). Speech-to-\n" +
			"text is configured via [voice] (default: typed text); text-to-speech\n" +
			"uses macOS `say` by default on darwin.",
		Example: "  qi voice --text\n" +
			"  qi voice --once \"What are my agents doing?\"\n" +
			"  qi voice --once \"Tell the Codex agent in the qi workspace to run the tests.\"",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rt, err := buildAgentRuntime(cfg)
			if err != nil {
				return err
			}
			svc := service.NewAgentService(rt)
			out := cmd.OutOrStdout()

			speaker, err := buildSpeaker(cfg, out, quiet)
			if err != nil {
				return err
			}
			opts := voice.Options{Env: voice.EnvFromOS(), DryRun: dryRun, WorkspaceAliases: cfg.Voice.WorkspaceAliases}
			if cfg.Voice.WaitTimeoutSeconds > 0 {
				opts.WaitTimeout = time.Duration(cfg.Voice.WaitTimeoutSeconds) * time.Second
			}

			if once != "" {
				loop := voice.NewLoop(svc, voice.NewTextTranscriber(strings.NewReader(""), nil), speaker, opts)
				// HandleUtterance speaks its replies through the loop's speaker.
				_, err := loop.HandleUtterance(cmd.Context(), once)
				return err
			}

			in, err := buildTranscriber(cfg, textMode, cmd.InOrStdin(), out)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "qi voice (runtime: %s). Say \"quit\" to stop.\n", rt.Name())
			return voice.NewLoop(svc, in, speaker, opts).Run(cmd.Context())
		},
	}
	cmd.Flags().BoolVar(&textMode, "text", false, "type utterances instead of speaking (overrides [voice] stt)")
	cmd.Flags().StringVar(&once, "once", "", "handle one utterance and exit")
	cmd.Flags().BoolVar(&quiet, "quiet", false, "print replies only; never speak aloud")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "resolve and announce the agent but never send or wait")
	return cmd
}

// buildTranscriber picks the speech-to-text path. "text" reads lines from
// stdin. "http" records a clip with ffmpeg and posts it to an OpenAI-
// compatible transcription endpoint; it is never selected implicitly.
func buildTranscriber(cfg config.Config, forceText bool, stdin io.Reader, out io.Writer) (voice.Transcriber, error) {
	mode := strings.ToLower(cfg.Voice.STT)
	if forceText || mode == "" || mode == "text" {
		return voice.NewTextTranscriber(stdin, out), nil
	}
	if mode != "http" {
		return nil, fmt.Errorf("[voice] stt = %q: want \"text\" or \"http\"", cfg.Voice.STT)
	}
	if cfg.Voice.STTURL == "" {
		return nil, fmt.Errorf("[voice] stt = \"http\" requires stt_url")
	}
	secs := cfg.Voice.RecordSeconds
	if secs <= 0 {
		secs = 8
	}
	device := cfg.Voice.RecordDevice
	if device == "" {
		device = "0"
	}
	apiKey := ""
	if cfg.Voice.STTAPIKeyEnv != "" {
		apiKey = os.Getenv(cfg.Voice.STTAPIKeyEnv)
	}
	rec := voice.FFmpegRecorder("ffmpeg", device, secs, runCommand)
	return &voice.HTTPTranscriber{
		URL:    cfg.Voice.STTURL,
		Model:  cfg.Voice.STTModel,
		APIKey: apiKey,
		Client: &http.Client{Timeout: 60 * time.Second},
		Record: rec,
		Prompt: out,
		Hint:   cfg.Voice.STTPrompt,
	}, nil
}

// buildSpeaker picks text-to-speech: always echo the reply to out; on
// darwin additionally speak via `say` unless [voice] tts = "echo"/"none" or
// --quiet. Off darwin `say` is unavailable, so echo only.
func buildSpeaker(cfg config.Config, out io.Writer, quiet bool) (voice.Speaker, error) {
	mode := strings.ToLower(cfg.Voice.TTS)
	if mode == "" {
		if runtime.GOOS == "darwin" {
			mode = "say"
		} else {
			mode = "echo"
		}
	}
	switch mode {
	case "none":
		return voice.NewEchoSpeaker(io.Discard), nil
	case "echo":
		return voice.NewEchoSpeaker(out), nil
	case "say":
		if quiet || runtime.GOOS != "darwin" {
			return voice.NewEchoSpeaker(out), nil
		}
		return voice.NewSaySpeaker(out, runCommand), nil
	}
	return nil, fmt.Errorf("[voice] tts = %q: want \"say\", \"echo\", or \"none\"", cfg.Voice.TTS)
}

// runCommand executes name with args (no shell), inheriting nothing but ctx.
// Shared by the recorder and the `say` speaker.
func runCommand(ctx context.Context, name string, args ...string) error {
	c := exec.CommandContext(ctx, name, args...)
	c.Stdout = io.Discard
	c.Stderr = io.Discard
	return c.Run()
}
