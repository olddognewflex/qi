package commands

import (
	"bytes"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"qi/internal/config"
	"qi/internal/index"
)

// healthyVault builds a config pointing at a temp vault with all expected
// subdirs present, and isolates the machine-local dirs (data/runtime/state) into
// the temp area so doctor sees no qid socket and no index by default.
func healthyVault(t *testing.T) config.Config {
	t.Helper()
	vault := t.TempDir()
	for _, sub := range []string{"00-inbox", "10-tasks", "20-notes", "30-daily"} {
		if err := os.MkdirAll(filepath.Join(vault, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "cfg"))

	return config.Config{
		VaultPath:    vault,
		TaskFilePath: filepath.Join(vault, "10-tasks", "inbox.md"),
		InboxPath:    filepath.Join(vault, "00-inbox"),
		NotesPath:    filepath.Join(vault, "20-notes"),
		DailyPath:    filepath.Join(vault, "30-daily"),
	}
}

func runDoctor(t *testing.T, cfg config.Config) (string, error) {
	t.Helper()
	cmd := newDoctorCommand(cfg)
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(nil)
	err := cmd.Execute()
	return buf.String(), err
}

func TestDoctor_HealthyVault(t *testing.T) {
	cfg := healthyVault(t)
	out, err := runDoctor(t, cfg)
	if err != nil {
		t.Fatalf("doctor returned error on healthy vault: %v\n%s", err, out)
	}
	if !strings.Contains(out, "all checks passed") {
		t.Fatalf("missing pass marker:\n%s", out)
	}
	for _, want := range []string{"[ok  ] vault path", "[ok  ] vault subdirs", "[warn] qid socket", "[warn] index", "[ok  ] worker"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing line %q in:\n%s", want, out)
		}
	}
}

func TestDoctor_MissingVaultFails(t *testing.T) {
	cfg := healthyVault(t)
	cfg.VaultPath = filepath.Join(cfg.VaultPath, "does-not-exist")
	out, err := runDoctor(t, cfg)
	if err == nil {
		t.Fatalf("expected failure for missing vault:\n%s", out)
	}
	if !strings.Contains(out, "[fail] vault path") {
		t.Fatalf("missing fail marker:\n%s", out)
	}
}

func TestDoctor_MissingSubdirsWarn(t *testing.T) {
	cfg := healthyVault(t)
	// Remove a subdir so it is reported missing but the vault itself is fine.
	if err := os.RemoveAll(cfg.NotesPath); err != nil {
		t.Fatalf("rm notes: %v", err)
	}
	out, err := runDoctor(t, cfg)
	if err != nil {
		t.Fatalf("missing subdir should warn, not fail: %v\n%s", err, out)
	}
	if !strings.Contains(out, "[warn] vault subdirs") || !strings.Contains(out, "notes") {
		t.Fatalf("expected subdir warning:\n%s", out)
	}
}

func TestDoctor_WorkerReachable(t *testing.T) {
	srv := httptest.NewServer((&fakeWorker{}).handler(t))
	t.Cleanup(srv.Close)

	cfg := healthyVault(t)
	cfg.RemoteQueue = config.RemoteQueueConfig{Enabled: true, URL: srv.URL, Token: drainToken}

	out, err := runDoctor(t, cfg)
	if err != nil {
		t.Fatalf("doctor failed with reachable worker: %v\n%s", err, out)
	}
	if !strings.Contains(out, "[ok  ] worker") {
		t.Fatalf("worker should be ok:\n%s", out)
	}
}

func TestDoctor_WorkerUnreachableFails(t *testing.T) {
	cfg := healthyVault(t)
	// Port 0 on a closed address: dial fails fast.
	cfg.RemoteQueue = config.RemoteQueueConfig{Enabled: true, URL: "http://127.0.0.1:1", Token: drainToken}

	out, err := runDoctor(t, cfg)
	if err == nil {
		t.Fatalf("expected failure for unreachable worker:\n%s", out)
	}
	if !strings.Contains(out, "[fail] worker") {
		t.Fatalf("worker should fail:\n%s", out)
	}
}

func TestDoctor_WorkerEnabledMissingURLFails(t *testing.T) {
	cfg := healthyVault(t)
	cfg.RemoteQueue = config.RemoteQueueConfig{Enabled: true, Token: drainToken}
	out, err := runDoctor(t, cfg)
	if err == nil {
		t.Fatalf("expected failure for empty url:\n%s", out)
	}
	if !strings.Contains(out, "enabled but url empty") {
		t.Fatalf("missing url-empty detail:\n%s", out)
	}
}

// rebuildIndex opens the index (under the test's XDG_DATA_HOME), rebuilds it
// from the vault, and closes it.
func rebuildIndex(t *testing.T, vaultPath string) {
	t.Helper()
	idx, err := index.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	if err := idx.Rebuild(vaultPath); err != nil {
		t.Fatal(err)
	}
}

// touchLater sets path's mtime comfortably after any index write so far.
func touchLater(t *testing.T, path string) {
	t.Helper()
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
}

func TestDoctor_IndexFreshAfterRebuild(t *testing.T) {
	cfg := healthyVault(t)
	if err := os.WriteFile(filepath.Join(cfg.NotesPath, "a.md"), []byte("# A\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rebuildIndex(t, cfg.VaultPath)

	out, err := runDoctor(t, cfg)
	if err != nil {
		t.Fatalf("doctor failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "[ok  ] index — fresh (1 notes indexed") {
		t.Fatalf("expected fresh index with FTS row count:\n%s", out)
	}
}

func TestDoctor_IndexStaleAfterVaultWrite(t *testing.T) {
	cfg := healthyVault(t)
	if err := os.WriteFile(filepath.Join(cfg.NotesPath, "a.md"), []byte("# A\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rebuildIndex(t, cfg.VaultPath)

	// A note written after the rebuild: newest mtime > last_indexed marker.
	later := filepath.Join(cfg.NotesPath, "b.md")
	if err := os.WriteFile(later, []byte("# B\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	touchLater(t, later)

	out, err := runDoctor(t, cfg)
	if err != nil {
		t.Fatalf("stale index should warn, not fail: %v\n%s", err, out)
	}
	if !strings.Contains(out, "[warn] index — stale") {
		t.Fatalf("expected stale index warning:\n%s", out)
	}
}

// TestDoctor_TaskSyncWriteDoesNotFakeIndexFreshness is the issue #44
// regression test: a task-sync commit bumps the db FILE's mtime without
// touching the FTS table, and doctor must still call the index stale.
func TestDoctor_TaskSyncWriteDoesNotFakeIndexFreshness(t *testing.T) {
	cfg := healthyVault(t)
	if err := os.WriteFile(filepath.Join(cfg.NotesPath, "a.md"), []byte("# A\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rebuildIndex(t, cfg.VaultPath)

	// Vault changes AFTER the rebuild...
	later := filepath.Join(cfg.NotesPath, "b.md")
	if err := os.WriteFile(later, []byte("# B\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	touchLater(t, later)

	// ...then a task-sync write bumps the db file's mtime. Under the old
	// db-mtime heuristic this flipped the report back to "fresh".
	idx, err := index.Open()
	if err != nil {
		t.Fatal(err)
	}
	if err := idx.CommitSyncState([]index.SyncBase{
		{ID: "qi-test", Project: "p", BaseLine: "- [ ] x"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	idx.Close()

	out, err := runDoctor(t, cfg)
	if err != nil {
		t.Fatalf("doctor failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "[warn] index — stale") {
		t.Fatalf("task-sync write must not mask staleness:\n%s", out)
	}
}

func TestDoctor_IndexMarkerAdvancedByUpsert(t *testing.T) {
	cfg := healthyVault(t)
	if err := os.WriteFile(filepath.Join(cfg.NotesPath, "a.md"), []byte("# A\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rebuildIndex(t, cfg.VaultPath)

	// A note written after the rebuild, then incrementally indexed (what the
	// qid watcher does): the marker advances and the index is fresh again
	// without a full rebuild.
	later := filepath.Join(cfg.NotesPath, "b.md")
	if err := os.WriteFile(later, []byte("# B\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	idx, err := index.Open()
	if err != nil {
		t.Fatal(err)
	}
	if err := idx.UpsertFile(later); err != nil {
		t.Fatal(err)
	}
	idx.Close()

	out, err := runDoctor(t, cfg)
	if err != nil {
		t.Fatalf("doctor failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "[ok  ] index — fresh (2 notes indexed") {
		t.Fatalf("upsert should restore freshness with 2 rows:\n%s", out)
	}
}
