package commands

import (
	"github.com/spf13/cobra"
	"qi/internal/config"
)

// newRemoteCommand groups the cloud-queue operations under `qi remote` — a
// command family consistent with the rest of the CLI, replacing the hyphenated
// top-level `remote-status` / `remote-drain` (#61). Those old spellings survive
// as deprecated top-level aliases (deprecatedRemoteAliases) so muscle memory and
// scripts keep working while cobra nudges toward the new form.
func newRemoteCommand(cfg config.Config) *cobra.Command {
	remote := &cobra.Command{
		Use:   "remote",
		Short: "Inspect and drain the cloud capture queue",
	}
	remote.AddCommand(
		newRemoteStatusCommand(cfg, "status"),
		newRemoteDrainCommand(cfg, "drain"),
	)
	return remote
}

// deprecatedRemoteAliases returns the pre-#61 top-level spellings, kept working
// but marked deprecated so cobra prints a one-line pointer to `qi remote …`.
func deprecatedRemoteAliases(cfg config.Config) []*cobra.Command {
	status := newRemoteStatusCommand(cfg, "remote-status")
	status.Deprecated = "use \"qi remote status\""
	drain := newRemoteDrainCommand(cfg, "remote-drain")
	drain.Deprecated = "use \"qi remote drain\""
	return []*cobra.Command{status, drain}
}
