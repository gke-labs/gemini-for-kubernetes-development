package sandbox

import (
	"time"
)

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
