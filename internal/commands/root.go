package commands

import (
	"github.com/spf13/cobra"
	"qi/internal/config"
)

// skipConfigAnnotation marks a command (and its subcommands) as runnable without
// a valid config. Commands meant to rescue a fresh install — help, doctor,
// config, and init — set this so a missing vault_path never blocks them.
const skipConfigAnnotation = "qi.skipConfigCheck"

func NewRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "qi",
		Short: "qi is a local-first personal assistant",
		// Errors are printed once by main; a returned config/usage error is not
		// a reason to dump usage text.
		SilenceErrors: true,
		SilenceUsage:  true,
	}

	// Load config lazily rather than fatally in the constructor: a brand-new
	// user with no config must still be able to run `qi --help`, `qi doctor`,
	// `qi init`, and `qi config edit`. The load happens here (non-fatal) so the
	// value is available to every command's RunE; PersistentPreRunE below
	// enforces that commands genuinely needing a vault fail with the clear
	// error, while the rescue commands are exempt.
	cfg, cfgErr := config.Load()

	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		if cfgErr != nil && !skipsConfigCheck(cmd) {
			return cfgErr
		}
		return nil
	}

	root.AddCommand(newTaskCommand(cfg))
	root.AddCommand(newCaptureCommand(cfg))
	root.AddCommand(newNoteCommand(cfg))
	root.AddCommand(newIndexCommand(cfg))
	root.AddCommand(markSkipConfig(newConfigCommand()))
	root.AddCommand(markSkipConfig(newInitCommand()))
	root.AddCommand(newAgendaCommand(cfg))
	root.AddCommand(newCalendarCommand(cfg))
	root.AddCommand(newDailyCommand(cfg))
	root.AddCommand(newPlanCommand(cfg))
	root.AddCommand(newAICommand())
	root.AddCommand(markSkipConfig(newDaemonCommand()))
	root.AddCommand(newSyncCommand(cfg))
	root.AddCommand(newLaunchCommand(cfg))
	root.AddCommand(newRemoteDrainCommand(cfg))
	root.AddCommand(markSkipConfig(newDoctorCommand(cfg)))
	root.AddCommand(newRemoteStatusCommand(cfg))
	root.AddCommand(newInboxCommand(cfg))
	root.AddCommand(newSearchCommand(cfg))
	root.AddCommand(newEmbedCommand(cfg))
	return root
}

// markSkipConfig tags a command so PersistentPreRunE lets it run without a valid
// config. It returns the command for inline use in AddCommand.
func markSkipConfig(cmd *cobra.Command) *cobra.Command {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[skipConfigAnnotation] = "true"
	return cmd
}

// skipsConfigCheck reports whether cmd, or any ancestor, is exempt from the
// valid-config requirement. Walking ancestors means a subcommand like
// `qi config edit` inherits the exemption set on `config`.
func skipsConfigCheck(cmd *cobra.Command) bool {
	for c := cmd; c != nil; c = c.Parent() {
		if c.Annotations[skipConfigAnnotation] == "true" {
			return true
		}
	}
	return false
}
