package commands

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"qi/internal/config"
)

func NewRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "qi",
		Short: "qi is a local-first personal assistant",
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config error:", err)
		os.Exit(1)
	}

	root.AddCommand(newTaskCommand(cfg))
	root.AddCommand(newCaptureCommand(cfg))
	root.AddCommand(newNoteCommand(cfg))
	root.AddCommand(newIndexCommand(cfg))
	root.AddCommand(newConfigCommand())
	root.AddCommand(newAgendaCommand(cfg))
	root.AddCommand(newCalendarCommand(cfg))
	root.AddCommand(newDailyCommand(cfg))
	root.AddCommand(newPlanCommand(cfg))
	root.AddCommand(newAICommand())
	root.AddCommand(newSyncCommand(cfg))
	root.AddCommand(newLaunchCommand(cfg))
	root.AddCommand(newRemoteDrainCommand(cfg))
	root.AddCommand(newDoctorCommand(cfg))
	root.AddCommand(newRemoteStatusCommand(cfg))
	root.AddCommand(newInboxCommand(cfg))
	root.AddCommand(newSearchCommand(cfg))
	return root
}
