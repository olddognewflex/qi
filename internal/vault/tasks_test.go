package vault

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"qi/internal/domain"
)

func TestParseTaskLine(t *testing.T) {
	line := "- [ ] Fix CDK bootstrap #builderhq 📅 2026-04-22"
	task, ok, err := ParseTaskLine(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected task line to parse")
	}
	if task.Text != "Fix CDK bootstrap #builderhq" {
		t.Fatalf("unexpected text: %q", task.Text)
	}
	if task.Project != "builderhq" {
		t.Fatalf("unexpected project: %q", task.Project)
	}
	if task.Due == nil || task.Due.Format("2006-01-02") != "2026-04-22" {
		t.Fatalf("unexpected due: %#v", task.Due)
	}
	if task.Completed {
		t.Fatal("expected open task")
	}
}

func TestUpdateTaskLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.md")
	initial := "- [ ] Task one\n- [ ] Task two\n- [ ] Task three\n"
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := UpdateTaskLine(path, 2, domain.Task{Text: "Task two", Completed: true}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	if lines[0] != "- [ ] Task one" {
		t.Fatalf("line 1 changed: %q", lines[0])
	}
	if lines[1] != "- [x] Task two" {
		t.Fatalf("line 2: got %q, want %q", lines[1], "- [x] Task two")
	}
	if lines[2] != "- [ ] Task three" {
		t.Fatalf("line 3 changed: %q", lines[2])
	}
}

func TestFormatTaskLine(t *testing.T) {
	due := time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC)
	line, err := FormatTaskLine(domain.Task{
		Text:    "Fix CDK bootstrap",
		Project: "builderhq",
		Due:     &due,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "- [ ] Fix CDK bootstrap #builderhq 📅 2026-04-22"
	if line != want {
		t.Fatalf("got %q want %q", line, want)
	}
}

func TestFormatTaskLine_NoTagDuplicationOnRoundTrip(t *testing.T) {
	const line = "- [ ] Fix CDK bootstrap #builderhq 📅 2026-04-22"
	task, ok, err := ParseTaskLine(line)
	if err != nil || !ok {
		t.Fatalf("parse: ok=%v err=%v", ok, err)
	}
	task.Completed = true

	out, err := FormatTaskLine(task)
	if err != nil {
		t.Fatal(err)
	}
	const want = "- [x] Fix CDK bootstrap #builderhq 📅 2026-04-22"
	if out != want {
		t.Fatalf("tag duplicated on format: got %q want %q", out, want)
	}

	// Idempotent: parse→format must reach a fixed point, not grow the line.
	task2, ok, err := ParseTaskLine(out)
	if err != nil || !ok {
		t.Fatalf("reparse: ok=%v err=%v", ok, err)
	}
	out2, err := FormatTaskLine(task2)
	if err != nil {
		t.Fatal(err)
	}
	if out2 != want {
		t.Fatalf("not idempotent: %q -> %q", out, out2)
	}
}

func TestUpdateTaskLine_RejectsDrift(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.md")
	if err := os.WriteFile(path, []byte("- [ ] Task two\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Caller captured line 1 as "Task one", but the file now has "Task two"
	// there (e.g. an Obsidian edit between read and write).
	err := UpdateTaskLine(path, 1, domain.Task{Text: "Task one", Completed: true})
	if err == nil {
		t.Fatal("expected drifted line to be rejected")
	}
	if !strings.Contains(err.Error(), "changed since read") {
		t.Fatalf("unexpected error: %v", err)
	}

	data, _ := os.ReadFile(path)
	if string(data) != "- [ ] Task two\n" {
		t.Fatalf("file mutated despite drift: %q", string(data))
	}
}

// --- Phase 1: block-ref ID tests ---

// TestParseFormat_IDRoundTrip: a line with a qi id parses and reformats
// byte-identically (idempotent).
func TestParseFormat_IDRoundTrip(t *testing.T) {
	const line = "- [ ] Ship release #qi 📅 2026-05-27 ^qi-a1b2c3d4"
	task, ok, err := ParseTaskLine(line)
	if err != nil || !ok {
		t.Fatalf("parse: ok=%v err=%v", ok, err)
	}
	if task.ID != "qi-a1b2c3d4" {
		t.Fatalf("ID: got %q want %q", task.ID, "qi-a1b2c3d4")
	}
	out, err := FormatTaskLine(task)
	if err != nil {
		t.Fatalf("format: %v", err)
	}
	if out != line {
		t.Fatalf("round-trip mismatch:\n got  %q\n want %q", out, line)
	}
}

// TestParseFormat_NoIDUnchanged: a line with no id parses and reformats
// without any spurious id appended.
func TestParseFormat_NoIDUnchanged(t *testing.T) {
	const line = "- [ ] Fix CDK bootstrap #builderhq 📅 2026-04-22"
	task, ok, err := ParseTaskLine(line)
	if err != nil || !ok {
		t.Fatalf("parse: ok=%v err=%v", ok, err)
	}
	if task.ID != "" {
		t.Fatalf("expected empty ID, got %q", task.ID)
	}
	out, err := FormatTaskLine(task)
	if err != nil {
		t.Fatalf("format: %v", err)
	}
	if out != line {
		t.Fatalf("got %q want %q", out, line)
	}
}

// TestParseFormat_IDSurvivesEdit: id is preserved when text/due/tags are
// changed on the Task struct and it is reformatted.
func TestParseFormat_IDSurvivesEdit(t *testing.T) {
	const line = "- [ ] Ship release #qi 📅 2026-05-27 ^qi-a1b2c3d4"
	task, ok, err := ParseTaskLine(line)
	if err != nil || !ok {
		t.Fatalf("parse: ok=%v err=%v", ok, err)
	}
	// Edit text and due, keep the same ID.
	task.Text = "Ship final release #qi"
	newDue := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	task.Due = &newDue

	out, err := FormatTaskLine(task)
	if err != nil {
		t.Fatalf("format: %v", err)
	}
	const want = "- [ ] Ship final release #qi 📅 2026-06-01 ^qi-a1b2c3d4"
	if out != want {
		t.Fatalf("got %q want %q", out, want)
	}
}

// TestParseFormat_IDStrippedFromText: parsing a line with a qi id leaves
// Task.Text clean (no ^ref in Text), and reformatting does not double the id.
func TestParseFormat_IDStrippedFromText(t *testing.T) {
	const line = "- [ ] Do something ^qi-deadbeef"
	task, ok, err := ParseTaskLine(line)
	if err != nil || !ok {
		t.Fatalf("parse: ok=%v err=%v", ok, err)
	}
	if task.ID != "qi-deadbeef" {
		t.Fatalf("ID: got %q want %q", task.ID, "qi-deadbeef")
	}
	if strings.Contains(task.Text, "^") {
		t.Fatalf("Text contains caret: %q", task.Text)
	}

	// Format once — should end with exactly one ^qi-deadbeef.
	out, err := FormatTaskLine(task)
	if err != nil {
		t.Fatalf("format: %v", err)
	}
	if out != line {
		t.Fatalf("first format: got %q want %q", out, line)
	}

	// Format again from a re-parse — must not double the id.
	task2, ok, err := ParseTaskLine(out)
	if err != nil || !ok {
		t.Fatalf("reparse: ok=%v err=%v", ok, err)
	}
	out2, err := FormatTaskLine(task2)
	if err != nil {
		t.Fatalf("reformat: %v", err)
	}
	if out2 != line {
		t.Fatalf("idempotent check: got %q want %q", out2, line)
	}
}

// TestParseFormat_ForeignBlockRef: a line ending in a user's own ^ref (not
// qi format) → Task.ID stays "", the foreign ref is preserved in the output,
// and no second ref is appended.
func TestParseFormat_ForeignBlockRef(t *testing.T) {
	const line = "- [ ] My note reference ^myref"
	task, ok, err := ParseTaskLine(line)
	if err != nil || !ok {
		t.Fatalf("parse: ok=%v err=%v", ok, err)
	}
	if task.ID != "" {
		t.Fatalf("expected empty ID for foreign ref, got %q", task.ID)
	}
	// Text should still contain the foreign ^ref (we leave it untouched).
	if !strings.Contains(task.Text, "^myref") {
		t.Fatalf("foreign ref stripped from Text: %q", task.Text)
	}

	out, err := FormatTaskLine(task)
	if err != nil {
		t.Fatalf("format: %v", err)
	}
	// The output must contain the foreign ref and must NOT have a second ^.
	if !strings.Contains(out, "^myref") {
		t.Fatalf("foreign ref missing from output: %q", out)
	}
	// Count carets — there must be exactly one.
	if count := strings.Count(out, "^"); count != 1 {
		t.Fatalf("expected 1 caret in output, got %d: %q", count, out)
	}
}

// TestParseFormat_ForeignRefNotManaged: if a Task struct carries both an ID
// and its Text already ends with a foreign ^ref, FormatTaskLine refuses to
// append the qi id (collision rule).
func TestParseFormat_ForeignRefNotManaged(t *testing.T) {
	task := domain.Task{
		Text: "Some task ^userref",
		ID:   "qi-12345678",
	}
	out, err := FormatTaskLine(task)
	if err != nil {
		t.Fatalf("format: %v", err)
	}
	// Must contain the foreign ref but NOT the qi id.
	if !strings.Contains(out, "^userref") {
		t.Fatalf("foreign ref missing: %q", out)
	}
	if strings.Contains(out, "^qi-12345678") {
		t.Fatalf("qi id appended despite foreign ref collision: %q", out)
	}
	if count := strings.Count(out, "^"); count != 1 {
		t.Fatalf("expected 1 caret, got %d: %q", count, out)
	}
}

// --- Phase 2: ID minting + ID-based update tests ---

// TestMintID: returned string matches "qi-" + 8 lowercase hex chars and is
// unique across many calls.
func TestMintID(t *testing.T) {
	re := regexp.MustCompile(`^qi-[0-9a-f]{8}$`)
	seen := make(map[string]struct{}, 1000)
	for range 1000 {
		id := MintID()
		if !re.MatchString(id) {
			t.Fatalf("MintID returned %q, want qi-[0-9a-f]{8}", id)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("MintID collision: %q emitted twice in 1000 calls", id)
		}
		seen[id] = struct{}{}
	}
}

// TestUpdateTaskLine_ByID: write a task line with an ID, then update it with
// a new Text but the same ID. The line should be rewritten correctly.
func TestUpdateTaskLine_ByID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.md")
	const id = "qi-aabbccdd"
	original := "- [ ] Original text ^" + id + "\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	updated := domain.Task{
		ID:        id,
		Text:      "Renamed text",
		Completed: true,
	}
	if err := UpdateTaskLine(path, 1, updated); err != nil {
		t.Fatalf("UpdateTaskLine by ID: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.TrimRight(string(data), "\n")
	want := "- [x] Renamed text ^" + id
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

// TestUpdateTaskLine_ByIDRejectsMismatch: when task.ID is set but the file
// line carries a different ID, the update is rejected.
func TestUpdateTaskLine_ByIDRejectsMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.md")
	if err := os.WriteFile(path, []byte("- [ ] Some task ^qi-11111111\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := UpdateTaskLine(path, 1, domain.Task{
		ID:        "qi-22222222",
		Text:      "Some task",
		Completed: true,
	})
	if err == nil {
		t.Fatal("expected ID mismatch to be rejected")
	}
	if !strings.Contains(err.Error(), "ID changed since read") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestUpdateTaskLine_LegacyTextMatch: when task.ID is empty, the existing
// Text-match behaviour is preserved (regression guard).
func TestUpdateTaskLine_LegacyTextMatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.md")
	if err := os.WriteFile(path, []byte("- [ ] Legacy task\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := UpdateTaskLine(path, 1, domain.Task{Text: "Legacy task", Completed: true}); err != nil {
		t.Fatalf("legacy text match: %v", err)
	}

	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "- [x] Legacy task") {
		t.Fatalf("legacy text-match update did not apply: %q", string(data))
	}
}

// --- Recurrence (🔁) round-trip tests ---

func TestParseTaskLine_Recurrence(t *testing.T) {
	line := "- [ ] water plants 🔁 every week 📅 2026-06-12"
	task, ok, err := ParseTaskLine(line)
	if err != nil || !ok {
		t.Fatalf("parse: ok=%v err=%v", ok, err)
	}
	if task.Recurrence != "every week" {
		t.Fatalf("Recurrence: got %q want %q", task.Recurrence, "every week")
	}
	if task.Text != "water plants" {
		t.Fatalf("Text: got %q want %q", task.Text, "water plants")
	}
	if strings.Contains(task.Text, "🔁") {
		t.Fatalf("recurrence emoji leaked into Text: %q", task.Text)
	}
	if task.Due == nil || task.Due.Format("2006-01-02") != "2026-06-12" {
		t.Fatalf("unexpected due: %#v", task.Due)
	}
}

func TestFormatTaskLine_Recurrence(t *testing.T) {
	due := time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC)
	line, err := FormatTaskLine(domain.Task{
		Text:       "water plants",
		Recurrence: "every week",
		Due:        &due,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "- [ ] water plants 🔁 every week 📅 2026-06-12"
	if line != want {
		t.Fatalf("got %q want %q", line, want)
	}
}

func TestRecurrence_RoundTripStable(t *testing.T) {
	cases := []string{
		"- [ ] review backlog #work 🔁 every 2 weeks ⏳ 2026-06-10 📅 2026-06-12 ^qi-a1b2c3d4",
		"- [ ] stand up 🔁 every day when done ⏳ 2026-06-10 📅 2026-06-11 ^qi-deadbeef",
	}
	for _, line := range cases {
		task, ok, err := ParseTaskLine(line)
		if err != nil || !ok {
			t.Fatalf("parse %q: ok=%v err=%v", line, ok, err)
		}
		out1, err := FormatTaskLine(task)
		if err != nil {
			t.Fatalf("format1 %q: %v", line, err)
		}
		task2, ok, err := ParseTaskLine(out1)
		if err != nil || !ok {
			t.Fatalf("reparse %q: ok=%v err=%v", out1, ok, err)
		}
		out2, err := FormatTaskLine(task2)
		if err != nil {
			t.Fatalf("format2 %q: %v", out1, err)
		}
		if out1 != out2 {
			t.Fatalf("not idempotent:\n out1 %q\n out2 %q", out1, out2)
		}
		if task2.Recurrence != task.Recurrence {
			t.Fatalf("recurrence drift: %q -> %q", task.Recurrence, task2.Recurrence)
		}
		if task.Recurrence == "" {
			t.Fatalf("expected recurrence to survive in %q", line)
		}
		if (task.Due == nil) != (task2.Due == nil) || (task.Scheduled == nil) != (task2.Scheduled == nil) {
			t.Fatalf("date presence drift for %q", line)
		}
		if task2.Text != task.Text {
			t.Fatalf("text drift: %q -> %q", task.Text, task2.Text)
		}
	}
}

func TestRecurrence_WithTag(t *testing.T) {
	const line = "- [ ] pay rent #home 🔁 every month 📅 2026-07-01"
	task, ok, err := ParseTaskLine(line)
	if err != nil || !ok {
		t.Fatalf("parse: ok=%v err=%v", ok, err)
	}
	if task.Project != "home" {
		t.Fatalf("Project: got %q want %q", task.Project, "home")
	}
	if task.Recurrence != "every month" {
		t.Fatalf("Recurrence: got %q want %q", task.Recurrence, "every month")
	}
	out, err := FormatTaskLine(task)
	if err != nil {
		t.Fatalf("format: %v", err)
	}
	if out != line {
		t.Fatalf("round-trip mismatch:\n got  %q\n want %q", out, line)
	}
	if strings.Count(out, "#home") != 1 {
		t.Fatalf("tag duplicated: %q", out)
	}
}

func TestRecurrence_NoneFormatsWithoutEmoji(t *testing.T) {
	due := time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC)
	line, err := FormatTaskLine(domain.Task{
		Text:    "Fix CDK bootstrap",
		Project: "builderhq",
		Due:     &due,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(line, "🔁") {
		t.Fatalf("non-recurring task gained a 🔁 marker: %q", line)
	}
	want := "- [ ] Fix CDK bootstrap #builderhq 📅 2026-04-22"
	if line != want {
		t.Fatalf("got %q want %q", line, want)
	}
}

func TestUpdateTaskLine_ConcurrentDistinctLinesNoLostUpdate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.md")
	const n = 16
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, "- [ ] task-%02d\n", i)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	tasks, err := ReadTasks(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != n {
		t.Fatalf("seed read %d tasks, want %d", len(tasks), n)
	}

	// Complete every task concurrently, each targeting its own line. Without
	// cross-process locking each goroutine rewrites the whole file from a stale
	// snapshot and clobbers the others — only a fraction of completions
	// survive. The lock makes each read-guard-write atomic so all n land.
	var wg sync.WaitGroup
	for _, tk := range tasks {
		wg.Add(1)
		go func(tk domain.Task) {
			defer wg.Done()
			done := tk
			done.Completed = true
			_ = UpdateTaskLine(tk.FilePath, tk.LineNumber, done)
		}(tk)
	}
	wg.Wait()

	got, err := ReadTasks(path)
	if err != nil {
		t.Fatal(err)
	}
	completed := 0
	for _, tk := range got {
		if tk.Completed {
			completed++
		}
	}
	if completed != n {
		t.Fatalf("completed %d/%d tasks — lost updates under concurrency", completed, n)
	}
}
