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
	"qi/internal/daemon"
	"qi/internal/daemon/client"
)

const dialTimeout = 500 * time.Millisecond

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
			fmt.Fprintf(os.Stderr, "qid not reachable at %s; start it with `qi daemon start`\n", path)
		}
		return nil, err
	}
	return c, nil
}
