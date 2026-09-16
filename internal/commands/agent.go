package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"qi/internal/agentrt"
	"qi/internal/config"
	"qi/internal/service"
)

// buildAgentRuntime is the single place a command turns [agents] config into
// an agentrt.Runtime: "auto" (default) detects Herdr from the environment,
// "herdr"/"local" pin one. A package var so tests can substitute a fake.
var buildAgentRuntime = func(cfg config.Config) (agentrt.Runtime, error) {
	name := cfg.Agents.Runtime
	if name == "" {
		name = "auto"
	}
	bin := cfg.Agents.HerdrBin
	if bin == "" {
		bin = os.Getenv("HERDR_BIN_PATH")
	}
	return agentrt.ForName(name, bin)
}

// agentTargetFlags is the shared "who do you mean" flag set for `qi agent
// send|read|wait`. It maps 1:1 onto service.AgentQuery so the CLI and the
// voice grammar resolve through the same path.
type agentTargetFlags struct {
	kind      string
	workspace string
	name      string
	id        string
	focused   bool
	this      bool
}

func (f *agentTargetFlags) bind(cmd *cobra.Command) {
	cmd.Flags().StringVarP(&f.kind, "kind", "k", "", "agent kind (claude|codex|...)")
	cmd.Flags().StringVarP(&f.workspace, "workspace", "w", "", "workspace label or id (e.g. qi, w9)")
	cmd.Flags().StringVar(&f.name, "name", "", "runtime-assigned unique agent name")
	cmd.Flags().StringVar(&f.id, "id", "", "explicit runtime handle (pane id, e.g. w9:p1)")
	cmd.Flags().BoolVar(&f.focused, "focused", false, "the agent in the UI-focused pane")
	cmd.Flags().BoolVar(&f.this, "this", false, "an agent in this workspace ($HERDR_WORKSPACE_ID, else the current directory)")
}

// validate rejects flag combinations Resolve would silently drop: an
// explicit --id is already unambiguous, so any other constraint alongside it
// is either redundant or contradictory, and a stated constraint must never be
// ignored.
func (f *agentTargetFlags) validate() error {
	if f.id != "" && (f.kind != "" || f.workspace != "" || f.name != "" || f.focused || f.this) {
		return fmt.Errorf("--id cannot be combined with --kind, --workspace, --name, --focused, or --this")
	}
	return nil
}

func (f *agentTargetFlags) query() service.AgentQuery {
	q := service.AgentQuery{
		Kind:      service.ParseKind(f.kind),
		Workspace: f.workspace,
		Name:      f.name,
		ID:        agentrt.AgentID(f.id),
		Focused:   f.focused,
	}
	if f.this && q.Workspace == "" {
		if ws := os.Getenv("HERDR_WORKSPACE_ID"); ws != "" {
			q.Workspace = ws
		} else if cwd, err := os.Getwd(); err == nil {
			q.Cwd = cwd
		}
	}
	return q
}

// resolveAgent runs the query and renders ambiguity/no-match as a clean
// error (the clarification prompt for ambiguity, so the user can add a
// narrowing flag) instead of a Go error dump.
func resolveAgent(ctx context.Context, svc *service.AgentService, tf *agentTargetFlags) (agentrt.AgentInstance, error) {
	if err := tf.validate(); err != nil {
		return agentrt.AgentInstance{}, err
	}
	q := tf.query()
	inst, err := svc.Resolve(ctx, q)
	var amb *service.AmbiguousAgentError
	if errors.As(err, &amb) {
		return inst, fmt.Errorf("%s (narrow with --workspace, --name, --id, or --focused)", amb.Prompt())
	}
	var none *service.NoAgentError
	if errors.As(err, &none) && svc.Runtime().Name() == "local" {
		return inst, fmt.Errorf("%s No agent runtime was detected: start Herdr, or set [agents] runtime", none.Error())
	}
	return inst, err
}

func newAgentCommand(cfg config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Discover and address coding agents (Claude Code, Codex) via the agent runtime",
		Long: "Lists, addresses, and observes coding-agent instances managed by a runtime\n" +
			"such as Herdr. qi reasons about instances, not agent types: two Claude Code\n" +
			"processes are two agents with distinct panes and sessions. Ambiguous\n" +
			"targets are reported, never guessed. Herdr is detected automatically\n" +
			"(HERDR_ENV=1 or a running herdr server); without it no agents are known.",
		Example: "  qi agent list\n" +
			"  qi agent status\n" +
			"  qi agent send --kind codex --workspace qi \"run the tests\" --wait\n" +
			"  qi agent send --focused \"run the tests\"\n" +
			"  qi agent read --kind claude --this --lines 60\n" +
			"  qi agent wait --id w9:p3 --until idle --timeout 2m",
	}
	cmd.AddCommand(newAgentListCommand(cfg))
	cmd.AddCommand(newAgentStatusCommand(cfg))
	cmd.AddCommand(newAgentSendCommand(cfg))
	cmd.AddCommand(newAgentReadCommand(cfg))
	cmd.AddCommand(newAgentWaitCommand(cfg))
	return cmd
}

func newAgentListCommand(cfg config.Config) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List live agent instances across workspaces",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rt, err := buildAgentRuntime(cfg)
			if err != nil {
				return err
			}
			svc := service.NewAgentService(rt)
			agents, err := svc.List(cmd.Context())
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(cmd, agentsToJSON(rt.Name(), agents))
			}
			out := cmd.OutOrStdout()
			if len(agents) == 0 {
				fmt.Fprintf(out, "No agents found (runtime: %s).\n", rt.Name())
				return nil
			}
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "KIND\tNAME\tSTATE\tWORKSPACE\tPANE\tSESSION\tCWD")
			for _, a := range agents {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
					a.DisplayKind(), dash(a.Name), a.State, dash(a.Workspace.Label), a.PaneID, dash(shortID(a.SessionID)), dash(a.Cwd))
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit stable JSON")
	return cmd
}

func newAgentStatusCommand(cfg config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Summarize what each agent is doing, one sentence per agent",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rt, err := buildAgentRuntime(cfg)
			if err != nil {
				return err
			}
			agents, err := service.NewAgentService(rt).List(cmd.Context())
			if err != nil {
				return err
			}
			for _, line := range service.SummarizeAgents(agents) {
				fmt.Fprintln(cmd.OutOrStdout(), line)
			}
			return nil
		},
	}
}

func newAgentSendCommand(cfg config.Config) *cobra.Command {
	var tf agentTargetFlags
	var wait bool
	var timeout time.Duration
	var lines int
	cmd := &cobra.Command{
		Use:   "send <instruction...>",
		Short: "Send an instruction to one resolved agent instance",
		Long: "Resolves exactly one agent from the target flags and submits the instruction\n" +
			"as a prompt. Refuses (with the candidates) when more than one agent matches.\n" +
			"An agent blocked on an approval/question is not written to. With --wait, qi\n" +
			"waits for the agent to settle and prints its recent output.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rt, err := buildAgentRuntime(cfg)
			if err != nil {
				return err
			}
			svc := service.NewAgentService(rt)
			ctx := cmd.Context()
			inst, err := resolveAgent(ctx, svc, &tf)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			text := strings.Join(args, " ")
			fmt.Fprintf(out, "Found %s. Sending the request.\n", service.DescribeAgent(inst))
			if err := svc.Instruct(ctx, inst.ID, text); err != nil {
				if errors.Is(err, agentrt.ErrAgentBlocked) {
					return fmt.Errorf("%s is blocked on an approval or question in pane %s; handle it first", inst.DisplayKind(), inst.PaneLabel())
				}
				return err
			}
			if !wait {
				return nil
			}
			wctx := ctx
			if timeout > 0 {
				var cancel context.CancelFunc
				wctx, cancel = context.WithTimeout(ctx, timeout)
				defer cancel()
			}
			res, err := svc.AwaitResult(wctx, inst.ID, lines)
			if err != nil {
				if errors.Is(err, agentrt.ErrTimeout) {
					fmt.Fprintf(out, "%s is still working after %s; stopped waiting.\n", inst.DisplayKind(), timeout)
					return nil
				}
				return err
			}
			fmt.Fprintf(out, "%s is %s.\n", inst.DisplayKind(), res.State)
			if res.Output != "" {
				fmt.Fprintln(out, strings.TrimRight(res.Output, "\n"))
			}
			return nil
		},
	}
	tf.bind(cmd)
	cmd.Flags().BoolVar(&wait, "wait", false, "wait for the agent to settle, then print its recent output")
	cmd.Flags().DurationVar(&timeout, "timeout", 5*time.Minute, "how long --wait waits (0 = indefinitely)")
	cmd.Flags().IntVar(&lines, "lines", 40, "output lines to print after --wait")
	return cmd
}

func newAgentReadCommand(cfg config.Config) *cobra.Command {
	var tf agentTargetFlags
	var lines int
	cmd := &cobra.Command{
		Use:   "read",
		Short: "Print an agent's recent terminal output",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rt, err := buildAgentRuntime(cfg)
			if err != nil {
				return err
			}
			svc := service.NewAgentService(rt)
			inst, err := resolveAgent(cmd.Context(), svc, &tf)
			if err != nil {
				return err
			}
			o, err := rt.ReadOutput(cmd.Context(), inst.ID, agentrt.OutputOptions{Lines: lines})
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), strings.TrimRight(o.Text, "\n"))
			return nil
		},
	}
	tf.bind(cmd)
	cmd.Flags().IntVar(&lines, "lines", 40, "lines of recent output")
	return cmd
}

func newAgentWaitCommand(cfg config.Config) *cobra.Command {
	var tf agentTargetFlags
	var until []string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "wait",
		Short: "Block until an agent reaches a state (default: idle, done, or blocked)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rt, err := buildAgentRuntime(cfg)
			if err != nil {
				return err
			}
			svc := service.NewAgentService(rt)
			inst, err := resolveAgent(cmd.Context(), svc, &tf)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			if timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, timeout)
				defer cancel()
			}
			states := make([]agentrt.State, 0, len(until))
			for _, s := range until {
				states = append(states, agentrt.State(strings.ToLower(s)))
			}
			st, err := rt.WaitForState(ctx, inst.ID, states...)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s is %s.\n", service.DescribeAgent(inst), st)
			return nil
		},
	}
	tf.bind(cmd)
	cmd.Flags().StringSliceVar(&until, "until", nil, "state to wait for (repeatable): idle|working|blocked|done|unknown")
	cmd.Flags().DurationVar(&timeout, "timeout", 0, "give up after this long (0 = wait indefinitely)")
	return cmd
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// shortID trims a UUID-ish session id to its first 8 chars for tables.
func shortID(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}
