package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResolveQidBinary(t *testing.T) {
	notFound := func(string) (string, error) { return "", errors.New("not found in PATH") }
	onPath := func(string) (string, error) { return "/usr/local/bin/qid", nil }
	// exists reports true for exactly the paths listed, standing in for a stat
	// plus an executable-bit check.
	exists := func(paths ...string) func(string) bool {
		set := make(map[string]bool, len(paths))
		for _, p := range paths {
			set[p] = true
		}
		return func(p string) bool { return set[p] }
	}
	sibling := filepath.Join("/opt/qi/bin", qidBinName())

	tests := []struct {
		name       string
		flag       string
		env        string
		exeDir     string
		executable func(string) bool
		lookPath   func(string) (string, error)
		want       string
		wantErr    string
	}{
		{
			name:       "flag wins over everything",
			flag:       "/custom/qid",
			env:        "/env/qid",
			exeDir:     "/opt/qi/bin",
			executable: exists("/custom/qid", "/env/qid", sibling),
			lookPath:   onPath,
			want:       "/custom/qid",
		},
		{
			name:       "env wins over sibling and path",
			env:        "/env/qid",
			exeDir:     "/opt/qi/bin",
			executable: exists("/env/qid", sibling),
			lookPath:   onPath,
			want:       "/env/qid",
		},
		{
			name:       "sibling wins over path",
			exeDir:     "/opt/qi/bin",
			executable: exists(sibling),
			lookPath:   onPath,
			want:       sibling,
		},
		{
			name:       "falls back to PATH when no sibling",
			exeDir:     "/opt/qi/bin",
			executable: exists(),
			lookPath:   onPath,
			want:       "/usr/local/bin/qid",
		},
		{
			name:       "no exe dir falls back to PATH",
			executable: exists(),
			lookPath:   onPath,
			want:       "/usr/local/bin/qid",
		},
		{
			name:       "nothing resolves",
			exeDir:     "/opt/qi/bin",
			executable: exists(),
			lookPath:   notFound,
			wantErr:    "qid binary not found",
		},
		{
			name:       "explicit flag that is not executable errors",
			flag:       "/custom/qid",
			exeDir:     "/opt/qi/bin",
			executable: exists(sibling),
			lookPath:   onPath,
			wantErr:    "--qid-bin",
		},
		{
			name:       "explicit env that is not executable errors",
			env:        "/env/qid",
			exeDir:     "/opt/qi/bin",
			executable: exists(sibling),
			lookPath:   onPath,
			wantErr:    QidBinEnv,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveQidBinary(tt.flag, tt.env, tt.exeDir, tt.executable, tt.lookPath)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got %q", tt.wantErr, got)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIsExecutableFile(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "plain")
	if err := os.WriteFile(plain, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	runnable := filepath.Join(dir, "runnable")
	if err := os.WriteFile(runnable, []byte("x"), 0o700); err != nil {
		t.Fatal(err)
	}

	if isExecutableFile(filepath.Join(dir, "missing")) {
		t.Error("missing file reported executable")
	}
	if isExecutableFile(dir) {
		t.Error("directory reported executable")
	}
	if !isExecutableFile(runnable) {
		t.Error("0700 file not reported executable")
	}
	// On Windows the permission bits carry no meaning, so any regular file counts.
	if got := isExecutableFile(plain); got != (qidBinName() == "qid.exe") {
		t.Errorf("isExecutableFile(0600 file) = %v", got)
	}
}

func TestBinaryStaleness(t *testing.T) {
	base := time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		binModTime time.Time
		startedAt  time.Time
		want       Staleness
	}{
		{"binary older than daemon start", base.Add(-time.Hour), base, StalenessCurrent},
		{"binary rebuilt after daemon start", base.Add(time.Hour), base, StalenessStale},
		{"identical timestamps are current", base, base, StalenessCurrent},
		{"unknown binary mtime", time.Time{}, base, StalenessUnknown},
		{"unknown start time", base, time.Time{}, StalenessUnknown},
		{"both unknown", time.Time{}, time.Time{}, StalenessUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := BinaryStaleness(tt.binModTime, tt.startedAt); got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStalenessOf(t *testing.T) {
	base := time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC)
	older, newer := base.Add(-time.Hour), base.Add(time.Hour)

	tests := []struct {
		name           string
		daemonExe      string
		resolvedExe    string
		binModTime     time.Time
		startedAt      time.Time
		want           Staleness
		wantMismatched bool
	}{
		{
			name:           "different binary wins over a current mtime",
			daemonExe:      "/usr/local/bin/qid",
			resolvedExe:    "/home/u/.local/bin/qid",
			binModTime:     older,
			startedAt:      base,
			want:           StalenessStale,
			wantMismatched: true,
		},
		{
			name:        "same binary falls through to the mtime check",
			daemonExe:   "/usr/local/bin/qid",
			resolvedExe: "/usr/local/bin/qid",
			binModTime:  older,
			startedAt:   base,
			want:        StalenessCurrent,
		},
		{
			name:        "same binary rebuilt after start is stale",
			daemonExe:   "/usr/local/bin/qid",
			resolvedExe: "/usr/local/bin/qid",
			binModTime:  newer,
			startedAt:   base,
			want:        StalenessStale,
		},
		{
			name:        "unknown daemon exe skips the mismatch check",
			resolvedExe: "/home/u/.local/bin/qid",
			binModTime:  older,
			startedAt:   base,
			want:        StalenessCurrent,
		},
		{
			name:       "unresolvable qi-side binary skips the mismatch check",
			daemonExe:  "/usr/local/bin/qid",
			binModTime: older,
			startedAt:  base,
			want:       StalenessCurrent,
		},
		{
			name:        "mismatch is decided even when timestamps are unknown",
			daemonExe:   "/usr/local/bin/qid",
			resolvedExe: "/home/u/.local/bin/qid",
			want:        StalenessStale, wantMismatched: true,
		},
		{
			name:        "matching binary with unknown timestamps is unknown",
			daemonExe:   "/usr/local/bin/qid",
			resolvedExe: "/usr/local/bin/qid",
			want:        StalenessUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, mismatched := StalenessOf(tt.daemonExe, tt.resolvedExe, tt.binModTime, tt.startedAt)
			if got != tt.want || mismatched != tt.wantMismatched {
				t.Fatalf("got (%v, %v), want (%v, %v)", got, mismatched, tt.want, tt.wantMismatched)
			}
		})
	}
}

func TestAliveAndWaitGone(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "qid.sock")
	if Alive(sock) {
		t.Fatal("Alive on a nonexistent socket")
	}
	if !WaitGone(sock, time.Second) {
		t.Fatal("WaitGone on a nonexistent socket")
	}

	ln, err := Listen(sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	if !Alive(sock) {
		t.Fatal("Alive false while listening")
	}
	if WaitGone(sock, 200*time.Millisecond) {
		t.Fatal("WaitGone true while listening")
	}
	_ = ln.Close()
	_ = os.Remove(sock)
	if !WaitGone(sock, time.Second) {
		t.Fatal("WaitGone false after close")
	}
}

func TestLogPathSitsBesideSocket(t *testing.T) {
	got := LogPath(filepath.Join("/run/user/501/qi", "qid.sock"))
	want := filepath.Join("/run/user/501/qi", "qid.log")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
