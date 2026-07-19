package commands

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// freshConfigEnv points config resolution at an empty temp XDG dir with no
// vault, simulating a brand-new install.
func freshConfigEnv(t *testing.T) string {
	t.Helper()
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	t.Setenv("QI_VAULT_PATH", "")
	t.Setenv("QI_TASK_FILE_PATH", "")
	return cfgHome
}

func runRoot(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := NewRootCommand()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

// A missing config must NOT abort the rescue commands.
func TestRoot_HelpRunsWithoutConfig(t *testing.T) {
	freshConfigEnv(t)
	out, err := runRoot(t, "--help")
	if err != nil {
		t.Fatalf("qi --help errored on fresh install: %v", err)
	}
	if !strings.Contains(out, "init") {
		t.Errorf("help output missing init command:\n%s", out)
	}
}

func TestRoot_InitRunsWithoutConfig(t *testing.T) {
	cfgHome := freshConfigEnv(t)
	out, err := runRoot(t, "init", "--vault", "/tmp/example-vault")
	if err != nil {
		t.Fatalf("qi init errored on fresh install: %v", err)
	}
	path := filepath.Join(cfgHome, "qi", "config.toml")
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("init did not write config at %s: %v", path, statErr)
	}
	if !strings.Contains(out, "Wrote starter config") {
		t.Errorf("unexpected init output:\n%s", out)
	}
}

// A vault-requiring command must still fail with the clear config error.
func TestRoot_VaultCommandErrorsWithoutConfig(t *testing.T) {
	freshConfigEnv(t)
	_, err := runRoot(t, "task", "list")
	if err == nil {
		t.Fatal("qi task list unexpectedly succeeded without a vault")
	}
	if !strings.Contains(err.Error(), "vault_path is required") {
		t.Errorf("error = %q, want the clear vault_path message", err)
	}
}

func TestSkipsConfigCheck(t *testing.T) {
	root := NewRootCommand()
	for _, name := range []string{"init", "doctor", "config"} {
		cmd, _, err := root.Find([]string{name})
		if err != nil {
			t.Fatalf("Find(%q): %v", name, err)
		}
		if !skipsConfigCheck(cmd) {
			t.Errorf("%q should be exempt from the config check", name)
		}
	}
	// A regular command must NOT be exempt.
	cmd, _, err := root.Find([]string{"task"})
	if err != nil {
		t.Fatalf("Find(task): %v", err)
	}
	if skipsConfigCheck(cmd) {
		t.Error("task should require config")
	}
	// `config edit` inherits the exemption from its parent.
	edit, _, err := root.Find([]string{"config", "edit"})
	if err != nil {
		t.Fatalf("Find(config edit): %v", err)
	}
	if !skipsConfigCheck(edit) {
		t.Error("config edit should inherit exemption from config")
	}
}
