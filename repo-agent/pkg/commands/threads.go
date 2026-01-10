package commands

import (
	"time"

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

type ThreadInfo struct {
	SessionID   string `json:"sessionId,omitempty"`
	ProjectHash string `json:"projectHash,omitempty"`

	StartTime time.Time `json:"startTime,omitempty"`

	TotalTokens int `json:"tokens,omitempty"`

	Messages []ThreadMessage `json:"messages,omitempty"`

	Workspace string `json:"workspace,omitempty"`
}

type ThreadMessage struct {
	ID        string    `json:"id,omitempty"`
	Timestamp time.Time `json:"timestamp,omitempty"`
	Type      string    `json:"type,omitempty"`
	Content   string    `json:"content,omitempty"`
	Model     string    `json:"model,omitempty"`

	ToolCalls []ToolCall `json:"toolCalls,omitempty"`
}

type ToolCall struct {
	ID        string         `json:"id,omitempty"`
	Name      string         `json:"name,omitempty"`
	Arguments map[string]any `json:"arguments,omitempty"`
}
