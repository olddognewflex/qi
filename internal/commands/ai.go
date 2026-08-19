package commands

import "github.com/spf13/cobra"

func newAICommand() *cobra.Command {
	command := &cobra.Command{Use: "ai", Short: "AI orchestration commands (talk to qid)"}
	command.AddCommand(
		newAIToolsCommand(),
		newAIApprovalsCommand(),
		newAIApproveCommand(),
		newAIDenyCommand(),
		newAIRunCommand(),
		newAIResumeCommand(),
	)
	return command
}
