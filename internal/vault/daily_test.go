package vault

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeNote(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "2026-05-24.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestReplaceSection_CreateOnNewFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fresh.md") // does not exist yet

	if err := ReplaceSection(path, "Logs", "- [09:00] started"); err != nil {
		t.Fatal(err)
	}

	body, found, err := ReadSection(path, "Logs")
	if err != nil || !found {
		t.Fatalf("ReadSection found=%v err=%v", found, err)
	}
	if body != "- [09:00] started" {
		t.Errorf("body = %q", body)
	}
	if got := read(t, path); !strings.HasSuffix(got, "\n") || strings.HasSuffix(got, "\n\n") {
		t.Errorf("want single trailing newline, got %q", got)
	}
}

func TestReplaceSection_PlacesAgendaAboveLogs(t *testing.T) {
	path := writeNote(t, "# 2026-05-24\n\n## Logs\n- [09:00] started\n")

	if err := ReplaceSection(path, "Agenda", "- 10:00 Standup"); err != nil {
		t.Fatal(err)
	}

	got := read(t, path)
	agendaIdx := strings.Index(got, "## Agenda")
	logsIdx := strings.Index(got, "## Logs")
	if agendaIdx < 0 || logsIdx < 0 {
		t.Fatalf("missing sections in:\n%s", got)
	}
	if agendaIdx > logsIdx {
		t.Errorf("Agenda must be above Logs, got:\n%s", got)
	}
	// Existing Logs content must be untouched.
	if body, _, _ := ReadSection(path, "Logs"); body != "- [09:00] started" {
		t.Errorf("Logs body changed: %q", body)
	}
}

func TestReplaceSection_AppendsWhenNoLogs(t *testing.T) {
	path := writeNote(t, "# 2026-05-24\n\nSome preamble.\n")

	if err := ReplaceSection(path, "Agenda", "- 10:00 Standup"); err != nil {
		t.Fatal(err)
	}
	got := read(t, path)
	if !strings.Contains(got, "## Agenda") {
		t.Fatalf("Agenda not written:\n%s", got)
	}
	if strings.Index(got, "Some preamble.") > strings.Index(got, "## Agenda") {
		t.Errorf("Agenda should append after existing content:\n%s", got)
	}
}

func TestReplaceSection_Idempotent(t *testing.T) {
	path := writeNote(t, "## Agenda\n- old\n\n## Logs\n- [09:00] x\n")

	if err := ReplaceSection(path, "Agenda", "- new"); err != nil {
		t.Fatal(err)
	}
	first := read(t, path)
	if err := ReplaceSection(path, "Agenda", "- new"); err != nil {
		t.Fatal(err)
	}
	if second := read(t, path); first != second {
		t.Errorf("not idempotent:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
	if body, _, _ := ReadSection(path, "Agenda"); body != "- new" {
		t.Errorf("Agenda body = %q, want - new", body)
	}
	if strings.Count(read(t, path), "## Agenda") != 1 {
		t.Error("duplicate Agenda section after overwrite")
	}
}

func TestAppendToSection_CreatesAndAppends(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "note.md")
	if err := os.WriteFile(path, []byte("# 2026-05-24\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := AppendToSection(path, "Logs", "- [09:00] first"); err != nil {
		t.Fatal(err)
	}
	if err := AppendToSection(path, "Logs", "- [10:30] second"); err != nil {
		t.Fatal(err)
	}

	body, found, err := ReadSection(path, "Logs")
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	want := "- [09:00] first\n- [10:30] second"
	if body != want {
		t.Errorf("Logs body = %q, want %q", body, want)
	}
}

func TestReadSection_Absent(t *testing.T) {
	path := writeNote(t, "# 2026-05-24\n\n## Logs\n- x\n")
	_, found, err := ReadSection(path, "Summary")
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Error("found=true for absent section")
	}
}

func TestReadSection_MissingFile(t *testing.T) {
	_, found, err := ReadSection("/nonexistent/note.md", "Logs")
	if err != nil {
		t.Errorf("missing file should not error, got %v", err)
	}
	if found {
		t.Error("found=true for missing file")
	}
}
