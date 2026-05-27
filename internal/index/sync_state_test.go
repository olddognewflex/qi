package index

import (
	"testing"
)

func TestSyncState_LoadEmpty(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	idx, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()

	m, err := idx.LoadSyncState()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(m) != 0 {
		t.Errorf("expected empty sync state, got %d", len(m))
	}
}

func TestSyncState_CommitUpsertDelete(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	idx, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()

	// Insert two.
	err = idx.CommitSyncState([]SyncBase{
		{ID: "qi-00000001", Project: "foo", BaseLine: "- [ ] a #foo ^qi-00000001"},
		{ID: "qi-00000002", Project: "bar", BaseLine: "- [ ] b #bar ^qi-00000002"},
	}, nil)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	m, _ := idx.LoadSyncState()
	if len(m) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(m))
	}
	if m["qi-00000001"].Project != "foo" {
		t.Errorf("project mismatch: %+v", m["qi-00000001"])
	}

	// Upsert (change project + line) and delete the other in one tx.
	err = idx.CommitSyncState([]SyncBase{
		{ID: "qi-00000001", Project: "baz", BaseLine: "- [ ] a #baz ^qi-00000001"},
	}, []string{"qi-00000002"})
	if err != nil {
		t.Fatalf("commit2: %v", err)
	}

	m, _ = idx.LoadSyncState()
	if len(m) != 1 {
		t.Fatalf("expected 1 row after delete, got %d", len(m))
	}
	if m["qi-00000001"].Project != "baz" {
		t.Errorf("upsert did not update project: %+v", m["qi-00000001"])
	}
	if _, ok := m["qi-00000002"]; ok {
		t.Errorf("qi-00000002 should be deleted")
	}
}
