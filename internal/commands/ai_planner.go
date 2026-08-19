package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"qi/internal/ai"
	"qi/internal/config"
	"qi/internal/daemon/client"
)

type plannerSessionStore interface {
	AcquireLease(ai.SessionID) (*ai.SessionLease, error)
	Load(ai.SessionID) (ai.Session, error)
	Delete(ai.SessionID) error
	Close() error
}

var newPlannerSessionStore = func() (plannerSessionStore, error) {
	return ai.DefaultSessionStore()
}

func newAIRunCommand() *cobra.Command {
	var socketFlag, modelFlag, providerFlag string
	command := &cobra.Command{
		Use:   "run <prompt>",
		Short: "Run a prompt through the AI planner (calls tools via qid)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			qid, err := dialClient(socketFlag)
			if err != nil {
				return err
			}
			defer qid.Close()
			planner, err := buildPlanner(qid, providerFlag, modelFlag)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(command.Context(), 5*time.Minute)
			defer cancel()
			result, err := planner.Run(ctx, joinArgs(args))
			if err != nil {
				return err
			}
			printPlannerResult(command.OutOrStdout(), result, socketFlag)
			return nil
		},
	}
	command.Flags().StringVar(&socketFlag, "socket", "", "qid socket path override")
	command.Flags().StringVar(&providerFlag, "provider", "", "LLM provider: anthropic|ollama|openai|kimi|opencode|zai (overrides config)")
	command.Flags().StringVar(&modelFlag, "model", "", "model id (overrides provider default + config)")
	return command
}

func newAIResumeCommand() *cobra.Command {
	var socketFlag, modelFlag, providerFlag string
	command := &cobra.Command{
		Use:   "resume <session-id>",
		Short: "Resume a planner session after its pending mutation was approved",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return resumePlanner(command, args[0], socketFlag, providerFlag, modelFlag)
		},
	}
	command.Flags().StringVar(&socketFlag, "socket", "", "qid socket path override")
	command.Flags().StringVar(&providerFlag, "provider", "", "replace the saved provider chain")
	command.Flags().StringVar(&modelFlag, "model", "", "override the model for a saved single provider")
	return command
}

func resumePlanner(command *cobra.Command, rawID, socketFlag, providerFlag, modelFlag string) (returnErr error) {
	sessionID, err := ai.ParseSessionID(rawID)
	if err != nil {
		return err
	}
	store, err := newPlannerSessionStore()
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, store.Close()) }()
	lease, err := store.AcquireLease(sessionID)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, lease.Release()) }()
	session, err := store.Load(sessionID)
	if err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	llm, err := buildResumeLLM(
		cfg,
		session.Provider,
		providerFlag,
		modelFlag,
		command.Flags().Changed("provider"),
		command.Flags().Changed("model"),
	)
	if err != nil {
		return err
	}
	qid, err := dialClient(socketFlag)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, qid.Close()) }()
	planner := ai.NewForResume(qid, llm, session.SessionID)
	ctx, cancel := context.WithTimeout(command.Context(), 5*time.Minute)
	defer cancel()
	result, err := planner.Resume(ctx, session)
	if err != nil {
		return err
	}
	if result.StopReason != ai.StopAwaitingApproval {
		if err := store.Delete(session.SessionID); err != nil {
			return fmt.Errorf("delete planner session: %w", err)
		}
		if err := lease.Complete(); err != nil {
			return fmt.Errorf("delete planner session lease: %w", err)
		}
	}
	printPlannerResult(command.OutOrStdout(), result, socketFlag)
	return nil
}

func buildPlanner(qid *client.Client, providerFlag, modelFlag string) (*ai.Planner, error) {
	llm, model, err := buildLLM(providerFlag, modelFlag)
	if err != nil {
		return nil, err
	}
	planner, err := ai.NewWithLLM(qid, llm)
	if err != nil {
		return nil, err
	}
	if model != "" {
		planner.SetModel(model)
	}
	return planner, nil
}

func printPlannerResult(out io.Writer, result ai.RunResult, socket string) {
	for _, turn := range result.Turns {
		if turn.Text != "" {
			fmt.Fprintf(out, "[turn %d] %s\n", turn.Iteration, turn.Text)
		}
		for _, call := range turn.ToolCalls {
			label := "✓"
			if call.Pending {
				label = "↻ approval"
			} else if call.Error != "" {
				label = "✗"
			}
			fmt.Fprintf(out, "  %s %s %s\n", label, call.Name, string(call.Input))
		}
	}
	if result.StopReason == ai.StopAwaitingApproval {
		socketArg := ""
		if socket != "" {
			socketArg = " --socket " + shellQuoteArg(socket)
		}
		fmt.Fprintln(out, "")
		fmt.Fprintln(out, "Approval required — approve, then resume to continue:")
		for _, pending := range result.Pending {
			reason := pending.Reason
			if reason == "" {
				reason = pending.ToolName
			}
			fmt.Fprintf(out, "  qi ai approve %s%s   # %s\n", pending.ApprovalID, socketArg, reason)
		}
		fmt.Fprintf(out, "  qi ai resume %s%s\n", result.SessionID, socketArg)
	}
	if result.StopReason == "max_iterations" {
		fmt.Fprintln(out, "(stopped: max iterations reached)")
	}
	usage := result.CacheUsage
	if usage.InputTokens+usage.OutputTokens > 0 {
		fmt.Fprintf(out, "\ntokens: in=%d out=%d cache_write=%d cache_read=%d\n",
			usage.InputTokens, usage.OutputTokens, usage.CacheCreationTokens, usage.CacheReadTokens)
	}
}

func shellQuoteArg(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
