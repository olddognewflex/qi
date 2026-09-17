package agentrt

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func envFrom(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func foundAt(path string) func(string) (string, error) {
	return func(string) (string, error) { return path, nil }
}

func notOnPath(name string) (string, error) {
	return "", errors.New("exec: \"" + name + "\": executable file not found in $PATH")
}

func statMissing(string) (fs.FileInfo, error) { return nil, fs.ErrNotExist }

func statFound(want string) statFunc {
	return func(p string) (fs.FileInfo, error) {
		if p != want {
			return nil, fs.ErrNotExist
		}
		return fakeFileInfo{}, nil
	}
}

type fakeFileInfo struct{ fs.FileInfo }

func TestDetect(t *testing.T) {
	cases := []struct {
		name     string
		env      map[string]string
		lookPath func(string) (string, error)
		stat     statFunc
		wantName string
		wantBin  string
	}{
		{
			name:     "inside a herdr pane uses HERDR_BIN_PATH",
			env:      map[string]string{"HERDR_ENV": "1", "HERDR_BIN_PATH": "/Users/me/.local/bin/herdr"},
			lookPath: notOnPath,
			stat:     statMissing,
			wantName: "herdr",
			wantBin:  "/Users/me/.local/bin/herdr",
		},
		{
			name:     "inside a herdr pane without HERDR_BIN_PATH falls back to PATH",
			env:      map[string]string{"HERDR_ENV": "1"},
			lookPath: notOnPath,
			stat:     statMissing,
			wantName: "herdr",
			wantBin:  "herdr",
		},
		{
			name:     "outside a pane with an explicit socket",
			env:      map[string]string{"HERDR_SOCKET_PATH": "/run/herdr.sock"},
			lookPath: foundAt("/usr/local/bin/herdr"),
			stat:     statMissing,
			wantName: "herdr",
			wantBin:  "/usr/local/bin/herdr",
		},
		{
			name:     "outside a pane with the default socket present",
			env:      map[string]string{"HOME": "/Users/me"},
			lookPath: foundAt("/usr/local/bin/herdr"),
			stat:     statFound(filepath.Join("/Users/me", ".config", "herdr", "herdr.sock")),
			wantName: "herdr",
			wantBin:  "/usr/local/bin/herdr",
		},
		{
			name:     "XDG_CONFIG_HOME wins over HOME for the default socket",
			env:      map[string]string{"HOME": "/Users/me", "XDG_CONFIG_HOME": "/xdg"},
			lookPath: foundAt("/usr/local/bin/herdr"),
			stat:     statFound(filepath.Join("/xdg", "herdr", "herdr.sock")),
			wantName: "herdr",
			wantBin:  "/usr/local/bin/herdr",
		},
		{
			name:     "binary installed but no server running",
			env:      map[string]string{"HOME": "/Users/me"},
			lookPath: foundAt("/usr/local/bin/herdr"),
			stat:     statMissing,
			wantName: "local",
		},
		{
			name:     "no binary at all",
			env:      map[string]string{"HOME": "/Users/me"},
			lookPath: notOnPath,
			stat:     statMissing,
			wantName: "local",
		},
		{
			name:     "empty environment",
			env:      map[string]string{},
			lookPath: notOnPath,
			stat:     statMissing,
			wantName: "local",
		},
		{
			name:     "HERDR_ENV set to something other than 1 is not a pane",
			env:      map[string]string{"HERDR_ENV": "0", "HOME": "/Users/me"},
			lookPath: foundAt("/usr/local/bin/herdr"),
			stat:     statMissing,
			wantName: "local",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := detect(envFrom(tc.env), tc.lookPath, tc.stat)
			if got.Name() != tc.wantName {
				t.Fatalf("Name() = %q, want %q", got.Name(), tc.wantName)
			}
			if tc.wantName == "herdr" {
				if bin := got.(*HerdrRuntime).bin; bin != tc.wantBin {
					t.Fatalf("bin = %q, want %q", bin, tc.wantBin)
				}
			}
		})
	}
}

func TestDetectDefaultAndDetectAreWired(t *testing.T) {
	// Detect delegates to detect with os.Stat; DetectDefault also reads the
	// real environment. Both must return a usable Runtime, never nil.
	if r := Detect(func(string) string { return "" }, notOnPath); r == nil || r.Name() != "local" {
		t.Fatalf("Detect = %v", r)
	}
	if r := DetectDefault(); r == nil {
		t.Fatal("DetectDefault returned nil")
	}
	// A socket that really exists on disk exercises the os.Stat path.
	dir := t.TempDir()
	sock := filepath.Join(dir, "herdr", "herdr.sock")
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	env := envFrom(map[string]string{"XDG_CONFIG_HOME": dir})
	if r := Detect(env, foundAt("/usr/local/bin/herdr")); r.Name() != "herdr" {
		t.Fatalf("Detect = %q, want herdr", r.Name())
	}
}

func TestDefaultSocketPath(t *testing.T) {
	if got := defaultSocketPath(envFrom(map[string]string{})); got != "" {
		t.Fatalf("no HOME/XDG = %q, want empty", got)
	}
}

func TestForName(t *testing.T) {
	for _, name := range []string{"herdr", "HERDR", " herdr "} {
		r, err := ForName(name, "/opt/herdr")
		if err != nil {
			t.Fatalf("ForName(%q): %v", name, err)
		}
		h, ok := r.(*HerdrRuntime)
		if !ok || h.bin != "/opt/herdr" {
			t.Fatalf("ForName(%q) = %#v", name, r)
		}
	}
	if r, err := ForName("local", ""); err != nil || r.Name() != "local" {
		t.Fatalf("ForName(local) = %v, %v", r, err)
	}
	for _, name := range []string{"auto", ""} {
		r, err := ForName(name, "")
		if err != nil || r == nil {
			t.Fatalf("ForName(%q) = %v, %v", name, r, err)
		}
	}
	if _, err := ForName("tmux", ""); err == nil {
		t.Fatal("ForName(tmux): want error")
	}
}
