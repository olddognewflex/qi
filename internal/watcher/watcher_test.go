package watcher_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"qi/internal/config"
	"qi/internal/watcher"
)

// writeFile writes content to name in dir, failing the test on error.
func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// startWatcher builds a Watcher over dir with a buffered signal channel returned
// to the caller, runs it under a cancelable context, and returns the signal
// channel plus the cancel func. It sleeps briefly so fsnotify registers the dir
// before the caller writes (warm-up only — not used for assertions).
func startWatcher(t *testing.T, dir string, debounce time.Duration) (<-chan struct{}, context.CancelFunc) {
	t.Helper()
	fired := make(chan struct{}, 8)
	w, err := watcher.New(watcher.Options{
		Dirs:     []string{dir},
		Debounce: debounce,
		OnChange: func() { fired <- struct{}{} },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = w.Run(ctx) }()
	// Warm-up: give fsnotify a moment to register the directory.
	time.Sleep(100 * time.Millisecond)
	return fired, cancel
}

func TestNew_Validation(t *testing.T) {
	if _, err := watcher.New(watcher.Options{Dirs: []string{"/tmp"}}); err == nil {
		t.Error("nil OnChange should error")
	}
	if _, err := watcher.New(watcher.Options{OnChange: func() {}}); err == nil {
		t.Error("empty Dirs should error")
	}
	if _, err := watcher.New(watcher.Options{Dirs: []string{"/tmp"}, OnChange: func() {}}); err != nil {
		t.Errorf("valid options should not error: %v", err)
	}
}

func TestRun_SingleWriteFiresOnce(t *testing.T) {
	dir := t.TempDir()
	fired, cancel := startWatcher(t, dir, 40*time.Millisecond)
	defer cancel()

	writeFile(t, dir, "task.md", "- [ ] hello")

	select {
	case <-fired:
		// good
	case <-time.After(time.Second):
		t.Fatal("expected onChange to fire within 1s")
	}

	// No second fire from a single write.
	select {
	case <-fired:
		t.Fatal("onChange fired a second time for a single write")
	case <-time.After(150 * time.Millisecond):
	}
}

func TestRun_BurstCoalesces(t *testing.T) {
	dir := t.TempDir()
	fired, cancel := startWatcher(t, dir, 60*time.Millisecond)
	defer cancel()

	for i := 0; i < 5; i++ {
		writeFile(t, dir, "f"+string(rune('a'+i))+".md", "- [ ] x")
	}

	// Collect fires over a window comfortably past the debounce.
	count := 0
	deadline := time.After(500 * time.Millisecond)
loop:
	for {
		select {
		case <-fired:
			count++
		case <-deadline:
			break loop
		}
	}
	if count < 1 {
		t.Fatalf("expected at least 1 fire for a burst, got %d", count)
	}
	if count > 2 {
		t.Fatalf("expected burst to coalesce (<=2 fires), got %d", count)
	}
}

func TestRun_IgnoresNonMarkdown(t *testing.T) {
	dir := t.TempDir()
	fired, cancel := startWatcher(t, dir, 40*time.Millisecond)
	defer cancel()

	writeFile(t, dir, "notes.txt", "not markdown")

	select {
	case <-fired:
		t.Fatal("onChange fired for a non-.md write")
	case <-time.After(250 * time.Millisecond):
		// good — no fire
	}
}

func TestRun_CtxCancelReturns(t *testing.T) {
	dir := t.TempDir()
	w, err := watcher.New(watcher.Options{
		Dirs:     []string{dir},
		Debounce: 40 * time.Millisecond,
		OnChange: func() {},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned %v, want nil on cancel", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not return promptly after cancel")
	}
}

func TestDirsFor_DedupesAndSorts(t *testing.T) {
	cfg := config.Config{
		TaskFilePath: "/vault/10-tasks/inbox.md",
		Projects: []config.ProjectConfig{
			{File: "/vault/10-tasks/bhq.md"},       // same dir as canon
			{File: "/other/projects/web/tasks.md"}, // distinct dir
			{File: "/other/projects/web/api.md"},   // duplicate dir
		},
	}
	got := watcher.DirsFor(cfg)
	want := []string{"/other/projects/web", "/vault/10-tasks"}
	if len(got) != len(want) {
		t.Fatalf("DirsFor = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("DirsFor[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
