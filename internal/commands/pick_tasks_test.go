package commands

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"qi/internal/domain"
)

func TestPickTasksNoTTYListsCandidates(t *testing.T) {
	// Force the headless path regardless of how the test is run.
	old := stdinIsTTY
	stdinIsTTY = func() bool { return false }
	t.Cleanup(func() { stdinIsTTY = old })

	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	candidates := []domain.Task{
		{Text: "write the report"},
		{Text: "write the tests"},
	}
	picked, err := pickTasks(cmd, "Tasks matching \"write\"", candidates)
	if err == nil {
		t.Fatal("expected an error without a terminal, got nil")
	}
	if picked != nil {
		t.Errorf("no selection should be returned, got %v", picked)
	}
	s := out.String()
	for _, want := range []string{"needs a terminal", "write the report", "write the tests", "more specific"} {
		if !strings.Contains(s, want) {
			t.Errorf("output missing %q:\n%s", want, s)
		}
	}
	if !strings.Contains(err.Error(), "ambiguous match") {
		t.Errorf("error = %q, want it to mention the ambiguous match", err)
	}
}
