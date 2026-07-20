package commands

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"qi/internal/config"
)

func newPrintCmd() (*cobra.Command, *bytes.Buffer) {
	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	return cmd, &buf
}

func TestPrintLaunchTargetText(t *testing.T) {
	cmd, buf := newPrintCmd()
	tgt := config.LaunchTarget{
		Harness:   config.LaunchConfig{Harness: "nvim", Args: []string{"--clean"}, Detach: false},
		VaultPath: "/vault",
		WorkDir:   "/dev/proj",
		Label:     `project "BHQ"`,
		FromEnv:   true,
	}
	if err := printLaunchTarget(cmd, tgt, []string{"file.md"}, false); err != nil {
		t.Fatalf("printLaunchTarget: %v", err)
	}
	out := buf.String()
	for _, want := range []string{`project "BHQ"`, "from $WORK_CONTEXT", "nvim --clean file.md", "detach:   false", "vault:    /vault", "cwd:      /dev/proj"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestPrintLaunchTargetTextGlobalDefaultInheritsCwd(t *testing.T) {
	cmd, buf := newPrintCmd()
	tgt := config.LaunchTarget{
		Harness:   config.LaunchConfig{Harness: "claude"},
		VaultPath: "/vault",
		// no Label, no WorkDir → global default, current dir inherited
	}
	if err := printLaunchTarget(cmd, tgt, nil, false); err != nil {
		t.Fatalf("printLaunchTarget: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "matched:  global default") {
		t.Errorf("expected global default label:\n%s", out)
	}
	if !strings.Contains(out, "cwd:      (current dir)") {
		t.Errorf("empty WorkDir should render as (current dir):\n%s", out)
	}
}

func TestPrintLaunchTargetJSON(t *testing.T) {
	cmd, buf := newPrintCmd()
	tgt := config.LaunchTarget{
		Harness:   config.LaunchConfig{Harness: "nvim", Args: []string{"--clean"}, Detach: true},
		VaultPath: "/vault",
		WorkDir:   "",
		Label:     `client "acme"`,
		FromEnv:   false,
	}
	if err := printLaunchTarget(cmd, tgt, []string{"a", "b"}, true); err != nil {
		t.Fatalf("printLaunchTarget: %v", err)
	}
	var got launchResolutionJSON
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output not valid JSON: %v\n%s", err, buf.String())
	}
	if got.Matched != `client "acme"` || got.Harness != "nvim" || !got.Detach {
		t.Errorf("json fields wrong: %+v", got)
	}
	if len(got.Args) != 1 || got.Args[0] != "--clean" {
		t.Errorf("args wrong: %+v", got.Args)
	}
	if len(got.Passthru) != 2 || got.Cwd != "" {
		t.Errorf("passthrough/cwd wrong: %+v", got)
	}
}
