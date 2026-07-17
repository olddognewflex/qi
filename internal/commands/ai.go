package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"qi/internal/ai"
	"qi/internal/config"
	"qi/internal/daemon"
	"qi/internal/daemon/client"
)

const dialTimeout = 500 * time.Millisecond

func newAICommand() *cobra.Command {
	ai := &cobra.Command{
		Use:   "ai",
		Short: "AI orchestration commands (talk to qid)",
	}
	ai.AddCommand(newAIToolsCommand())
	ai.AddCommand(newAIApprovalsCommand())
	ai.AddCommand(newAIApproveCommand())
	ai.AddCommand(newAIDenyCommand())
	ai.AddCommand(newAIRunCommand())
	return ai
}

func newAIRunCommand() *cobra.Command {
	var socketFlag, modelFlag, providerFlag string
	cmd := &cobra.Command{
		Use:   "run <prompt>",
		Short: "Run a prompt through the AI planner (calls tools via qid)",
		Long: "Sends the prompt to the configured LLM provider with qid's tool catalog, " +
			"executes proposed tool calls as caller=ai-planner:<id>. Mutating calls route " +
			"to the approval queue. Defaults to the [ai] section in config.toml; flags override.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			prompt := joinArgs(args)
			c, err := dialClient(socketFlag)
			if err != nil {
				return err
			}
			defer c.Close()

			planner, err := buildPlanner(c, providerFlag, modelFlag)
			if err != nil {
				return err
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
			defer cancel()
			res, err := planner.Run(ctx, prompt)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			for _, turn := range res.Turns {
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
			if len(res.Pending) > 0 {
				fmt.Fprintln(out, "")
				fmt.Fprintln(out, "Pending approvals:")
				for _, p := range res.Pending {
					fmt.Fprintf(out, "  qi ai approve %s   # %s\n", p.ApprovalID, p.Reason)
				}
			}
			if res.StopReason == "max_iterations" {
				fmt.Fprintln(out, "(stopped: max iterations reached)")
			}
			u := res.CacheUsage
			if u.InputTokens+u.OutputTokens > 0 {
				fmt.Fprintf(out, "\ntokens: in=%d out=%d cache_write=%d cache_read=%d\n",
					u.InputTokens, u.OutputTokens, u.CacheCreationTokens, u.CacheReadTokens)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&socketFlag, "socket", "", "qid socket path override")
	cmd.Flags().StringVar(&providerFlag, "provider", "", "LLM provider: anthropic|ollama|openai|kimi|opencode|zai (overrides config)")
	cmd.Flags().StringVar(&modelFlag, "model", "", "model id (overrides provider default + config)")
	return cmd
}

// buildPlanner picks an ai.Provider based on, in priority order:
//  1. --provider flag
//  2. QI_AI_PROVIDER env var
//  3. [ai].provider in config.toml
//  4. anthropic (final default)
//
// Model selection follows the same priority chain via flag → config →
// provider default.
func buildPlanner(c *client.Client, providerFlag, modelFlag string) (*ai.Planner, error) {
	llm, model, err := buildLLM(providerFlag, modelFlag)
	if err != nil {
		return nil, err
	}
	p := ai.NewWithLLM(c, llm)
	if model != "" {
		p.SetModel(model)
	}
	return p, nil
}

// buildLLM resolves an ai.LLM and its model. It does NOT touch qid, so
// non-planner AI paths (e.g. `qi task add --breakdown`) can reuse the same
// provider selection without a daemon. Selection, in priority order:
//
//  1. --provider flag / QI_AI_PROVIDER env — pins a single provider
//  2. [[ai.providers]] failover chain in config.toml (first = primary)
//  3. legacy [ai] provider/model keys
//  4. anthropic (final default)
//
// The returned model may be empty, in which case the provider applies its
// own default. With a failover chain the model is always empty — each chain
// entry stamps its own — so --model without --provider is rejected there.
func buildLLM(providerFlag, modelFlag string) (ai.LLM, string, error) {
	cfg, _ := config.Load()

	pinned, err := ai.ParseProvider(providerFlag)
	if err != nil {
		return nil, "", err
	}
	if pinned == "" {
		if pinned, err = ai.ParseProvider(os.Getenv("QI_AI_PROVIDER")); err != nil {
			return nil, "", err
		}
	}

	if pinned == "" && len(cfg.AI.Providers) > 0 {
		if modelFlag != "" {
			return nil, "", errors.New("--model is ambiguous with an [[ai.providers]] chain; pin a provider with --provider")
		}
		return buildFallbackLLM(cfg)
	}

	if pinned == "" {
		if pinned, err = ai.ParseProvider(cfg.AI.Provider); err != nil {
			return nil, "", err
		}
	}
	if pinned == "" {
		pinned = ai.ProviderAnthropic
	}

	entry, err := buildEntry(cfg, pinned, providerOverridesFor(cfg, pinned), modelFlag)
	if err != nil {
		return nil, "", err
	}
	return entry.LLM, entry.Model, nil
}

// buildFallbackLLM assembles the [[ai.providers]] chain. Unusable entries
// (unknown name, missing API key or model) are skipped with a stderr warning
// rather than failing the whole chain — a missing backup credential must not
// take down the primary.
func buildFallbackLLM(cfg config.Config) (ai.LLM, string, error) {
	entries := make([]ai.FallbackEntry, 0, len(cfg.AI.Providers))
	for _, pc := range cfg.AI.Providers {
		prov, err := ai.ParseProvider(pc.Provider)
		if err != nil || prov == "" {
			fmt.Fprintf(os.Stderr, "qi ai: skipping [[ai.providers]] entry: %v\n", err)
			continue
		}
		entry, err := buildEntry(cfg, prov, pc, "")
		if err != nil {
			fmt.Fprintf(os.Stderr, "qi ai: skipping provider %s: %v\n", pc.Provider, err)
			continue
		}
		entries = append(entries, entry)
	}
	fb, err := ai.NewFallbackLLM(entries, func(from, to ai.FallbackEntry, err error) {
		fmt.Fprintf(os.Stderr, "qi ai: provider %s failed (%v); falling back to %s\n", from.Name, err, to.Name)
	})
	if err != nil {
		return nil, "", fmt.Errorf("no usable [[ai.providers]] entries: %w", err)
	}
	return fb, "", nil
}

// providerOverridesFor finds the [[ai.providers]] entry matching a pinned
// provider so --provider still honors its configured url/model/key env.
// Returns a zero value when there is none.
func providerOverridesFor(cfg config.Config, prov ai.Provider) config.AIProviderConfig {
	for _, pc := range cfg.AI.Providers {
		if p, err := ai.ParseProvider(pc.Provider); err == nil && p == prov {
			return pc
		}
	}
	return config.AIProviderConfig{}
}

// buildEntry constructs one provider. modelOverride (the --model flag) wins
// over the [[ai.providers]] entry's model, which wins over the legacy [ai]
// keys. Anthropic and Ollama tolerate an empty model (provider default);
// the OpenAI-compatible providers require one, and require their API key
// env var to be set.
func buildEntry(cfg config.Config, prov ai.Provider, pc config.AIProviderConfig, modelOverride string) (ai.FallbackEntry, error) {
	model := modelOverride
	if model == "" {
		model = pc.Model
	}
	entry := ai.FallbackEntry{Name: string(prov)}

	switch prov {
	case ai.ProviderOllama:
		url := pc.URL
		if url == "" {
			url = os.Getenv("OLLAMA_URL")
		}
		if url == "" {
			url = cfg.AI.OllamaURL
		}
		if model == "" {
			model = cfg.AI.OllamaModel
		}
		entry.LLM = ai.NewOllamaProvider(url, os.Getenv("OLLAMA_API_KEY"), nil)
		entry.Model = model
		return entry, nil
	case ai.ProviderAnthropic:
		if model == "" {
			model = cfg.AI.Model
		}
		apiKey := ""
		if pc.APIKeyEnv != "" {
			if apiKey = os.Getenv(pc.APIKeyEnv); apiKey == "" {
				return entry, fmt.Errorf("%s not set", pc.APIKeyEnv)
			}
		}
		// An empty apiKey lets the SDK fall back to ANTHROPIC_API_KEY.
		entry.LLM = ai.NewAnthropicProvider(apiKey)
		entry.Model = model
		return entry, nil
	default:
		preset, ok := ai.PresetFor(prov)
		if !ok {
			return entry, fmt.Errorf("unknown ai provider %q", prov)
		}
		url := pc.URL
		if url == "" {
			url = preset.BaseURL
		}
		keyEnv := pc.APIKeyEnv
		if keyEnv == "" {
			keyEnv = preset.KeyEnv
		}
		apiKey := os.Getenv(keyEnv)
		if apiKey == "" {
			return entry, fmt.Errorf("%s not set", keyEnv)
		}
		if model == "" {
			return entry, fmt.Errorf("model is required for provider %q", prov)
		}
		entry.LLM = ai.NewOpenAIProvider(string(prov), url, apiKey, nil)
		entry.Model = model
		return entry, nil
	}
}

func joinArgs(args []string) string {
	out := ""
	for i, a := range args {
		if i > 0 {
			out += " "
		}
		out += a
	}
	return out
}

func newAIToolsCommand() *cobra.Command {
	var socketFlag string

	root := &cobra.Command{
		Use:   "tools",
		Short: "Inspect and invoke tools registered in qid",
	}
	root.PersistentFlags().StringVar(&socketFlag, "socket", "", "qid socket path override")

	list := &cobra.Command{
		Use:   "list",
		Short: "List tools registered in qid",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := dialClient(socketFlag)
			if err != nil {
				return err
			}
			defer c.Close()

			ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Second)
			defer cancel()
			listed, err := c.ListTools(ctx)
			if err != nil {
				return err
			}
			if len(listed) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "(no tools registered)")
				return nil
			}
			for _, t := range listed {
				flag := ""
				if t.Mutating {
					flag = " [mutating]"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s%s\n", t.Name, t.Source, flag)
				if t.Description != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "\t%s\n", t.Description)
				}
			}
			return nil
		},
	}

	var argsJSON, caller string
	call := &cobra.Command{
		Use:   "call <tool-name>",
		Short: "Invoke a tool through qid",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var arguments json.RawMessage
			if argsJSON != "" {
				if !json.Valid([]byte(argsJSON)) {
					return fmt.Errorf("--args is not valid JSON")
				}
				arguments = json.RawMessage(argsJSON)
			}

			c, err := dialClient(socketFlag)
			if err != nil {
				return err
			}
			defer c.Close()

			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			result, err := c.CallToolAs(ctx, caller, args[0], arguments)
			if err != nil {
				return err
			}
			if pending, ok := client.IsPending(result); ok {
				fmt.Fprintf(cmd.OutOrStdout(),
					"Approval required (id=%s).\n  reason: %s\n  approve: qi ai approve %s\n  deny:    qi ai deny %s\n",
					pending.ApprovalID, pending.Reason, pending.ApprovalID, pending.ApprovalID,
				)
				return nil
			}
			return printResult(cmd, result)
		},
	}
	call.Flags().StringVar(&argsJSON, "args", "", "tool arguments as JSON object (e.g. '{\"text\":\"hi\"}')")
	call.Flags().StringVar(&caller, "caller", client.CallerCLI, "caller identity (cli|ai|...) — non-cli mutations route through approval")

	root.AddCommand(list)
	root.AddCommand(call)
	return root
}

func newAIApprovalsCommand() *cobra.Command {
	var socketFlag, statusFlag string
	cmd := &cobra.Command{
		Use:   "approvals",
		Short: "List approvals queued in qid",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := dialClient(socketFlag)
			if err != nil {
				return err
			}
			defer c.Close()

			ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Second)
			defer cancel()
			list, err := c.ListApprovals(ctx, statusFlag)
			if err != nil {
				return err
			}
			if len(list) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "(no approvals)")
				return nil
			}
			for _, p := range list {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\tcaller=%s\n", p.ID, p.Status, p.ToolName, p.Caller)
				if p.Reason != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "\treason: %s\n", p.Reason)
				}
				if len(p.Params) > 0 {
					fmt.Fprintf(cmd.OutOrStdout(), "\tparams: %s\n", p.Params)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&socketFlag, "socket", "", "qid socket path override")
	cmd.Flags().StringVar(&statusFlag, "status", "", "filter by status (pending|approved|denied|executed|failed)")
	return cmd
}

func newAIApproveCommand() *cobra.Command {
	var socketFlag string
	cmd := &cobra.Command{
		Use:   "approve <id>",
		Short: "Approve a queued tool call and run it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := dialClient(socketFlag)
			if err != nil {
				return err
			}
			defer c.Close()

			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			p, err := c.ApproveAndRun(ctx, args[0])
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", p.ID, p.Status)
			if len(p.Result) > 0 {
				return printResult(cmd, p.Result)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&socketFlag, "socket", "", "qid socket path override")
	return cmd
}

func newAIDenyCommand() *cobra.Command {
	var socketFlag, reasonFlag string
	cmd := &cobra.Command{
		Use:   "deny <id>",
		Short: "Deny a queued tool call",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := dialClient(socketFlag)
			if err != nil {
				return err
			}
			defer c.Close()

			ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Second)
			defer cancel()
			p, err := c.DenyApproval(ctx, args[0], reasonFlag)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s\tdenied\n", p.ID)
			if reasonFlag != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "\treason: %s\n", reasonFlag)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&socketFlag, "socket", "", "qid socket path override")
	cmd.Flags().StringVar(&reasonFlag, "reason", "", "free-form denial reason for the audit log")
	return cmd
}

func printResult(cmd *cobra.Command, result json.RawMessage) error {
	if len(result) == 0 || string(result) == "null" {
		fmt.Fprintln(cmd.OutOrStdout(), "(no result)")
		return nil
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, result, "", "  "); err != nil {
		fmt.Fprintln(cmd.OutOrStdout(), string(result))
		return nil
	}
	fmt.Fprintln(cmd.OutOrStdout(), pretty.String())
	return nil
}

func dialClient(socketFlag string) (*client.Client, error) {
	path := socketFlag
	if path == "" {
		p, err := daemon.SocketPath()
		if err != nil {
			return nil, err
		}
		path = p
	}
	c, err := client.Dial(path, dialTimeout)
	if err != nil {
		if errors.Is(err, client.ErrDaemonUnavailable) {
			fmt.Fprintf(os.Stderr, "qid not reachable at %s; start it with `qid &`\n", path)
		}
		return nil, err
	}
	return c, nil
}
