package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

// ThreadsAgentOptions holds options for the ThreadsAgent function.
type ThreadsAgentOptions struct {
	SandboxName string

	FilterThreadID string

	IncludeMessages bool
}

// NewThreadsAgentCommand creates a new cobra command for managing LLM threads/chats in the dev sandbox.
func NewThreadsAgentCommand() *cobra.Command {
	var opt ThreadsAgentOptions

	cmd := &cobra.Command{
		Use:   "agent [sandbox-name]",
		Short: "Manage LLM threads/chats in the dev sandbox",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("threads agent command does not take any arguments")
			}
			return RunThreadsAgent(cmd.Context(), opt)
		},
	}
	cmd.Hidden = true

	cmd.Flags().StringVar(&opt.FilterThreadID, "thread-id", "", "If specified, filter only for the given thread ID")
	cmd.Flags().BoolVar(&opt.IncludeMessages, "include-messages", false, "If specified, include messages in the output")

	return cmd
}

// RunThreadsAgent runs the threads agent in the specified dev sandbox.
func RunThreadsAgent(ctx context.Context, opt ThreadsAgentOptions) error {
	agent := &threadsAgent{}

	threads, err := agent.listThreads(ctx, opt)
	if err != nil {
		return fmt.Errorf("failed to list threads: %w", err)
	}

	b, err := json.Marshal(threads)
	if err != nil {
		return fmt.Errorf("failed to marshal threads to JSON: %w", err)
	}
	if _, err := os.Stdout.Write(b); err != nil {
		return fmt.Errorf("failed to write threads to stdout: %w", err)
	}
	return nil
}

type threadsAgent struct {
}

type geminiSessionInfo struct {
	SessionID   string `json:"sessionId"`
	ProjectHash string `json:"projectHash"`

	StartTime   string `json:"startTime"`
	LastUpdated string `json:"lastUpdated"`

	Messages []geminiSessionMessage `json:"messages"`
}

type geminiSessionMessage struct {
	ID        string              `json:"id"`
	Timestamp time.Time           `json:"timestamp"`
	Type      string              `json:"type"`
	Content   string              `json:"content"`
	Thoughts  []json.RawMessage   `json:"thoughts"`
	Tokens    geminiSessionTokens `json:"tokens"`
	Model     string              `json:"model"`
	ToolCalls []geminiToolCall    `json:"toolCalls"`
}

type geminiSessionTokens struct {
	Input    int `json:"input"`
	Output   int `json:"output"`
	Cached   int `json:"cached"`
	Thoughts int `json:"thoughts"`
	Tool     int `json:"tool"`
	Total    int `json:"total"`
}

type geminiToolCall struct {
	ID                     string          `json:"id"`
	Name                   string          `json:"name"`
	Args                   map[string]any  `json:"args"`
	Result                 json.RawMessage `json:"result"`
	Status                 string          `json:"status"`
	Timestamp              string          `json:"timestamp"`
	ResultDisplay          string          `json:"resultDisplay"`
	DisplayName            string          `json:"displayName"`
	Description            string          `json:"description"`
	RenderOutputAsMarkdown bool            `json:"renderOutputAsMarkdown"`
}

func (a *threadsAgent) listThreads(ctx context.Context, opt ThreadsAgentOptions) ([]ThreadInfo, error) {
	log := klog.FromContext(ctx)

	var out []ThreadInfo

	// gemini --list-sessions only does one directory (the cwd), and doesn't output structured information.

	// TODO: Fix identity in dev-sandbox (it will likely trigger some alarms if we keep using root)
	geminiDir := "/root/.gemini/tmp"

	parseGeminiSessionFile := func(path string) error {
		if filepath.Base(path) == "logs.json" {
			// ignore
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("failed to read gemini session file %q: %w", path, err)
		}
		var session geminiSessionInfo
		if err := json.Unmarshal(b, &session); err != nil {
			return fmt.Errorf("failed to unmarshal gemini session file %q: %w", path, err)
		}

		if opt.FilterThreadID != "" && session.SessionID != opt.FilterThreadID {
			return nil
		}

		thread := ThreadInfo{
			SessionID:   session.SessionID,
			ProjectHash: session.ProjectHash,
		}
		if t, err := time.Parse(time.RFC3339, session.StartTime); err != nil {
			log.Error(err, "failed to parse start time", "startTime", session.StartTime, "path", path)
		} else {
			thread.StartTime = t
		}

		for _, msg := range session.Messages {
			thread.TotalTokens += msg.Tokens.Total
		}

		if opt.IncludeMessages {
			for _, msg := range session.Messages {
				msgOut := ThreadMessage{
					ID:        msg.ID,
					Timestamp: msg.Timestamp,
					Type:      msg.Type,
					Content:   msg.Content,
					Model:     msg.Model,
				}
				for _, toolCall := range msg.ToolCalls {
					toolCallOut := ToolCall{
						ID:        toolCall.ID,
						Name:      toolCall.Name,
						Arguments: toolCall.Args,
					}
					msgOut.ToolCalls = append(msgOut.ToolCalls, toolCallOut)
				}

				thread.Messages = append(thread.Messages, msgOut)
			}
		}

		out = append(out, thread)
		return nil
	}
	if err := filepath.WalkDir(geminiDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if filepath.Ext(name) != ".json" {
			return nil
		}
		if err := parseGeminiSessionFile(path); err != nil {
			log.Error(err, "failed to parse gemini session file", "path", path)
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("failed to walk gemini dir %q: %w", geminiDir, err)
	}

	return out, nil
}
