package commands

import (
	"testing"

	"qi/internal/config"
)

func TestRemoteGroupHasStatusAndDrain(t *testing.T) {
	remote := newRemoteCommand(config.Config{})
	for _, name := range []string{"status", "drain"} {
		if findCommand(remote, name) == nil {
			t.Errorf("qi remote %s is not registered", name)
		}
	}
}

func TestDeprecatedRemoteAliasesPointToNewForm(t *testing.T) {
	aliases := deprecatedRemoteAliases(config.Config{})
	byName := map[string]string{}
	for _, c := range aliases {
		byName[c.Name()] = c.Deprecated
	}
	for _, name := range []string{"remote-status", "remote-drain"} {
		msg, ok := byName[name]
		if !ok {
			t.Errorf("deprecated alias %q missing", name)
			continue
		}
		if msg == "" {
			t.Errorf("alias %q should be marked deprecated with a pointer", name)
		}
	}
}

func TestBareNoteShowsHelpNotSideEffect(t *testing.T) {
	note := newNoteCommand(config.Config{})
	// A group command with no Run is non-runnable → cobra prints help instead of
	// the old untitled-note side effect.
	if note.Runnable() {
		t.Error("bare `qi note` must not run an action (should show help)")
	}
	// The deprecated note search subcommand still exists but points elsewhere.
	if sc := findCommand(note, "search"); sc == nil || sc.Deprecated == "" {
		t.Error("note search should exist and be deprecated")
	}
}
