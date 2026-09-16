package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListenRejectsOverLongPath(t *testing.T) {
	// A path comfortably over any platform limit. The check is on the string
	// length and fires before any filesystem side effect, so the dir need not
	// exist.
	long := "/tmp/" + strings.Repeat("a", 130) + "/qid.sock"
	ln, err := Listen(long)
	if err == nil {
		_ = ln.Close()
		t.Fatal("expected an error for an over-length socket path")
	}
	for _, want := range []string{"over the", "OS limit", "shorter"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err, want)
		}
	}
}

func TestListenAcceptsShortPath(t *testing.T) {
	// A short path under a temp dir still binds (regression guard that the
	// length check doesn't reject normal paths).
	// os.MkdirTemp with a short prefix keeps the path well under the limit.
	dir, err := os.MkdirTemp("", "qs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "q.sock")
	if len(sock) > maxSocketPathLen {
		t.Skipf("temp socket path %q already exceeds the limit; environment too deep", sock)
	}
	ln, err := Listen(sock)
	if err != nil {
		t.Fatalf("Listen on a short path: %v", err)
	}
	_ = ln.Close()
}
