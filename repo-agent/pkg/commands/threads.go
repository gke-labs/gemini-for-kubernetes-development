package commands

import (
	"github.com/spf13/cobra"
)

// BuildThreadsCommand creates a new "parent" cobra command for threads/chats in the dev sandbox.
func BuildThreadsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "threads",
		Short: "Manage threads/chats in the dev sandbox",
	}

	cmd.AddCommand(NewThreadsListCommand())
	cmd.AddCommand(NewThreadsGetCommand())
	cmd.AddCommand(NewAppendToThreadCommand())

	// The agent is added as a hidden command.
	cmd.AddCommand(NewThreadsAgentCommand())

	return cmd
}
