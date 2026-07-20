package main

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"qi/internal/config"
	"qi/internal/skills"
	"qi/internal/tools"
	"qi/internal/tools/builtin"
)

// expectedMutating is the authoritative classification of every compiled-in
// builtin and deterministic skill tool qid registers. true = the tool writes to
// the vault; false = it is strictly read-only.
//
// This table is the single source of truth
// TestRegisteredToolsDeclareMutationExplicitly checks the live registry against,
// in BOTH directions: every registered tool must appear here, and every entry
// here must still be registered. A new tool therefore cannot reach qid's
// registry without a conscious mutation decision recorded here — the same
// anti-drift guarantee internal/calendar/build_test.go gives calendar kinds.
//
// Why it matters: invariant #3 (AI/MCP callers can never silently mutate the
// vault) depends entirely on each tool's Mutating flag. internal/policy routes
// a non-cli caller's mutation through the approval queue ONLY when the tool
// declares Mutating: true. A vault-writing tool that forgets the flag would let
// an ai-planner/mcp caller bypass the gate. This test makes that omission a
// build failure instead of a silent security hole.
var expectedMutating = map[string]bool{
	// builtins (SourceLocal)
	builtin.CaptureToolName:     true,  // writes a capture note to the inbox
	builtin.TaskAddToolName:     true,  // writes a task line
	builtin.TaskListToolName:    false, // read-only
	builtin.NoteSearchToolName:  false, // read-only
	builtin.AgendaTodayToolName: false, // read-only

	// skills (SourceSkill)
	skills.DailyReviewToolName:       false, // read-only aggregation
	skills.ProcessInboxToolName:      false, // read-only proposals
	skills.ProcessInboxApplyToolName: true,  // applies task/note/archive/delete
	skills.WeeklyReviewToolName:      false, // read-only aggregation
	skills.WeeklyReviewApplyToolName: true,  // writes the review note
	skills.QuickTaskToolName:         true,  // adds a task
	skills.SessionLogToolName:        true,  // appends to the daily note
}

// TestRegisteredToolsDeclareMutationExplicitly is the anti-drift guard for
// invariant #3. It builds the full builtin+skill registry exactly as qid wires
// it (registerTools — the same function run() calls), then cross-checks every
// tool's declared Mutating flag against expectedMutating in both directions.
//
// Adding a tool without classifying it here — or misdeclaring its Mutating flag
// — fails this test with a prescriptive message naming the tool, exactly like
// internal/calendar/build_test.go does for an unwired calendar kind.
func TestRegisteredToolsDeclareMutationExplicitly(t *testing.T) {
	registry := tools.NewRegistry()

	vault := t.TempDir()
	cfg := config.Config{
		VaultPath:    vault,
		TaskFilePath: filepath.Join(vault, "10-tasks", "inbox.md"),
		InboxPath:    filepath.Join(vault, "00-inbox"),
		NotesPath:    filepath.Join(vault, "20-notes"),
		DailyPath:    filepath.Join(vault, "30-daily"),
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	if err := registerTools(registry, cfg, log); err != nil {
		t.Fatalf("registerTools: %v", err)
	}

	got := registry.List()
	if len(got) == 0 {
		t.Fatal("registry is empty — has registerTools been gutted?")
	}

	// Direction 1: every registered tool must be classified, and its declared
	// Mutating flag must match the classification.
	seen := make(map[string]bool, len(got))
	for _, tool := range got {
		seen[tool.Name] = true
		want, ok := expectedMutating[tool.Name]
		if !ok {
			t.Errorf(unclassifiedToolMsg, tool.Name, tool.Mutating, tool.Name)
			continue
		}
		if tool.Mutating != want {
			t.Errorf(mutationMismatchMsg, tool.Name, tool.Mutating, want)
		}
	}

	// Direction 2: every classified tool must still be registered, so a renamed
	// or removed tool cannot leave a stale entry silently classifying nothing.
	for name := range expectedMutating {
		if !seen[name] {
			t.Errorf(staleClassificationMsg, name)
		}
	}
}

const unclassifiedToolMsg = `tool %q is registered but is not classified in expectedMutating (it declares Mutating=%v).

A new builtin or skill reached qid's registry without a conscious mutation
decision. Invariant #3 (AI/MCP callers can never silently mutate the vault)
depends on every tool declaring Mutating correctly:
  1. On the tool itself, set Mutating explicitly — true if it writes ANYTHING to
     the vault, false only if it is strictly read-only.
  2. Add %q to expectedMutating in cmd/qid/main_test.go with that same value.
Skipping step 1 for a vault-writing tool lets a non-cli caller bypass the
approval queue (internal/policy only gates tools that declare Mutating: true).`

const mutationMismatchMsg = `tool %q declares Mutating=%v but expectedMutating says %v.

Either the tool's Mutating flag is wrong or this test's table is stale. If the
tool writes to the vault it MUST declare Mutating: true so internal/policy routes
non-cli callers through the approval queue (invariant #3). Do NOT flip a mutating
tool to read-only, or relabel it here, just to make this test pass — that
reopens the exact silent-bypass hole the test exists to close.`

const staleClassificationMsg = `expectedMutating lists %q but no such tool is registered.

The tool was renamed or removed but this test's table still references it. Update
expectedMutating in cmd/qid/main_test.go to match the tools registerTools
actually registers.`
