package commands

import (
	"context"

	"github.com/spf13/cobra"
)

func NewIssueCommand(ctx context.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "issue",
		Short: "Manage GitHub issue workflows",
	}
	cmd.AddCommand(NewFixCommand(ctx))
	return cmd
}
