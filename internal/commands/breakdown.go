package commands

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"qi/internal/ai"
	"qi/internal/domain"
	"qi/internal/service"
)

// breakdownTimeout bounds a single AI breakdown call.
const breakdownTimeout = 60 * time.Second

// breakdownFn is the seam the `qi task add --breakdown` handler dispatches
// through, so command-level tests can assert flag parsing (the NoOptDefVal
// optional value resolves to the right level) without a live LLM. Production
// code leaves it pointed at runBreakdown.
var breakdownFn = runBreakdown

// runBreakdown is the shared orchestration for AI subtask breakdown, used by
// both `qi task add --breakdown` (QI-4) and `qi task breakdown` (QI-5). It calls
// llm to split parent.Text into subtasks at the given level, shows them for
// explicit approval, and — only on a "y" — writes each as a child task linked to
// parent via [parent:: parent.ID]. No silent mutation: the confirmation gate
// sits between the LLM proposal and any vault write, honoring qi's AI
// invariants.
//
// dryRun prints the proposals and stops before the gate (no writes). llm/model
// come from buildLLM at the call site (injected here so the gate is testable
// without a live provider). parent must already carry a non-empty ID (its
// subtasks link to it).
func runBreakdown(svc service.TaskService, parent domain.Task, level string, llm ai.LLM, model string, dryRun bool, in io.Reader, out io.Writer) error {
	if parent.ID == "" {
		return fmt.Errorf("cannot break down a task with no id")
	}
	if !ai.ValidLevel(level) {
		return fmt.Errorf("invalid breakdown level %q (want coarse|normal|fine)", level)
	}

	ctx, cancel := context.WithTimeout(context.Background(), breakdownTimeout)
	defer cancel()
	subtasks, err := ai.Breakdown(ctx, llm, model, parent.Text, level)
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "Proposed subtasks for %q (level: %s):\n", parent.Text, ai.NormalizeLevel(level))
	for i, s := range subtasks {
		fmt.Fprintf(out, "  %d. %s\n", i+1, s)
	}

	if dryRun {
		fmt.Fprintf(out, "\n%d subtask(s) proposed (dry-run, no writes)\n", len(subtasks))
		return nil
	}

	fmt.Fprintf(out, "\nCreate these %d subtask(s)? [y/N] ", len(subtasks))
	reader := bufio.NewReader(in)
	response, _ := reader.ReadString('\n')
	if strings.ToLower(strings.TrimSpace(response)) != "y" {
		fmt.Fprintln(out, "Aborted. No subtasks written.")
		return nil
	}

	created := 0
	for _, s := range subtasks {
		if _, err := svc.CreateTask(service.AddTaskInput{
			Text:     s,
			Project:  parent.Project,
			ParentID: parent.ID,
		}); err != nil {
			fmt.Fprintf(out, "skip %q: %v\n", s, err)
			continue
		}
		created++
		fmt.Fprintf(out, "  + %s\n", s)
	}
	fmt.Fprintf(out, "✓ Created %d subtask(s) under %q\n", created, parent.Text)
	return nil
}
