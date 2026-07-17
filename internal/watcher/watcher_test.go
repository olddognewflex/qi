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
func startWatcher(t *testing.T, dir string, debounce time.Duration) (<-chan []string, context.CancelFunc) {
	t.Helper()
	fired := make(chan []string, 8)
	w, err := watcher.New(watcher.Options{
		Dirs:     []string{dir},
		Debounce: debounce,
		OnChange: func(paths []string) { fired <- paths },
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
	if _, err := watcher.New(watcher.Options{OnChange: func([]string) {}}); err == nil {
		t.Error("empty Dirs should error")
	}
	if _, err := watcher.New(watcher.Options{Dirs: []string{"/tmp"}, OnChange: func([]string) {}}); err != nil {
		t.Errorf("valid options should not error: %v", err)
	}
}

func TestRun_SingleWriteFiresOnceWithPath(t *testing.T) {
	dir := t.TempDir()
	fired, cancel := startWatcher(t, dir, 40*time.Millisecond)
	defer cancel()

	writeFile(t, dir, "task.md", "- [ ] hello")

	select {
	case paths := <-fired:
		want := filepath.Join(dir, "task.md")
		found := false
		for _, p := range paths {
			if p == want {
				found = true
			}
		}
		if !found {
			t.Errorf("onChange paths = %v, want to contain %q", paths, want)
		}
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

func TestRun_BurstCoalescesAndCollectsPaths(t *testing.T) {
	dir := t.TempDir()
	fired, cancel := startWatcher(t, dir, 60*time.Millisecond)
	defer cancel()

	for i := 0; i < 5; i++ {
		writeFile(t, dir, "f"+string(rune('a'+i))+".md", "- [ ] x")
	}

	// Collect fires over a window comfortably past the debounce.
	count := 0
	pathSet := make(map[string]struct{})
	deadline := time.After(500 * time.Millisecond)
loop:
	for {
		select {
		case paths := <-fired:
			count++
			for _, p := range paths {
				pathSet[p] = struct{}{}
			}
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
	// All 5 distinct paths must survive coalescing — the whole point of
	// accumulating a set rather than dropping events on re-arm.
	if len(pathSet) != 5 {
		t.Errorf("collected %d distinct paths across fires, want 5: %v", len(pathSet), pathSet)
	}
}

func TestRun_RemoveFires(t *testing.T) {
	dir := t.TempDir()
	// Create before the watcher starts so the Create event isn't part of the test.
	writeFile(t, dir, "gone.md", "# bye")
	fired, cancel := startWatcher(t, dir, 40*time.Millisecond)
	defer cancel()

	if err := os.Remove(filepath.Join(dir, "gone.md")); err != nil {
		t.Fatal(err)
	}

	select {
	case paths := <-fired:
		want := filepath.Join(dir, "gone.md")
		found := false
		for _, p := range paths {
			if p == want {
				found = true
			}
		}
		if !found {
			t.Errorf("onChange paths = %v, want to contain removed %q", paths, want)
		}
	case <-time.After(time.Second):
		t.Fatal("expected onChange to fire for a removed .md within 1s")
	}
}

func TestRun_NewDirIsWatched(t *testing.T) {
	dir := t.TempDir()
	fired, cancel := startWatcher(t, dir, 40*time.Millisecond)
	defer cancel()

	// Create a subdir after the watcher started, then write a .md inside it.
	sub := filepath.Join(dir, "40-projects")
	if err := os.Mkdir(sub, 0755); err != nil {
		t.Fatal(err)
	}
	// Give the watcher a moment to process the dir-create and add the watch.
	time.Sleep(100 * time.Millisecond)
	writeFile(t, sub, "nested.md", "# nested")

	select {
	case paths := <-fired:
		want := filepath.Join(sub, "nested.md")
		found := false
		for _, p := range paths {
			if p == want {
				found = true
			}
		}
		if !found {
			t.Errorf("onChange paths = %v, want to contain %q", paths, want)
		}
	case <-time.After(time.Second):
		t.Fatal("expected onChange to fire for a write in a newly created subdir")
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
		OnChange: func([]string) {},
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

func TestVaultDirs_WalksAndSkipsHidden(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"20-notes", "40-projects/deep", ".obsidian/plugins", ".git"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0755); err != nil {
			t.Fatal(err)
		}
	}

	got := watcher.VaultDirs(root)
	want := []string{
		root,
		filepath.Join(root, "20-notes"),
		filepath.Join(root, "40-projects"),
		filepath.Join(root, "40-projects", "deep"),
	}
	if len(got) != len(want) {
		t.Fatalf("VaultDirs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("VaultDirs[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
