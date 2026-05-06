// Copyright 2026 The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package commands

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/sandbox"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

// ThreadsAgentOptions holds options for the ThreadsAgent function.
type ThreadsAgentOptions struct {
	SandboxName string

	// The ThreadID to operate on
	ThreadID string

	IncludeMessages bool

	// Action determines what action to perform: "list" or "append"
	Action string

	// Cwd is the current working directory for the agent
	Cwd string
}

func (o *ThreadsAgentOptions) InitDefaults() error {
	if o.Action == "" {
		o.Action = "list"
	}
	return nil
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
			if err := opt.InitDefaults(); err != nil {
				return err
			}
			return RunThreadsAgent(cmd.Context(), opt)
		},
	}
	cmd.Hidden = true

	cmd.Flags().StringVar(&opt.ThreadID, "thread-id", opt.ThreadID, "If specified, filter only for the given thread ID")
	cmd.Flags().StringVar(&opt.Action, "action", "list", "Action to perform: list or append")
	cmd.Flags().BoolVar(&opt.IncludeMessages, "include-messages", opt.IncludeMessages, "If specified, include messages in the output")
	cmd.Flags().StringVar(&opt.Cwd, "cwd", opt.Cwd, "Current working directory for the agent")

	return cmd
}

// RunThreadsAgent runs the threads agent in the specified dev sandbox.
func RunThreadsAgent(ctx context.Context, opt ThreadsAgentOptions) error {
	agent := &threadsAgent{}

	switch opt.Action {
	case "list":
		return agent.List(ctx, opt)
	case "append":
		return agent.Append(ctx, opt)

	default:
		return fmt.Errorf("unknown action %q", opt.Action)
	}
}

func (a *threadsAgent) List(ctx context.Context, opt ThreadsAgentOptions) error {
	threads, err := a.listThreads(ctx, opt)
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

func (a *threadsAgent) Append(ctx context.Context, opt ThreadsAgentOptions) error {
	log := klog.FromContext(ctx)

	if opt.ThreadID == "" {
		// TODO: Support creating a new thread?
		return fmt.Errorf("--thread-id is required for append action")
	}

	// Read stdin
	stdin, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("failed to read stdin: %w", err)
	}

	// Run gemini in yolo mode, resuming the specified thread

	// Use an uncancellable context to avoid issues with the connection being lost
	geminiCtx := context.WithoutCancel(ctx)

	cmd := exec.CommandContext(geminiCtx, "gemini", "--yolo",
		"--resume", opt.ThreadID,
	)
	cmd.Stdin = bytes.NewReader(stdin)
	if opt.Cwd != "" {
		cmd.Dir = opt.Cwd
	}

	// Our stdout/stderr go to the kubectl exec command
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	log.Info("Running gemini in yolo mode to append to thread", "threadID", opt.ThreadID)

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to run gemini in yolo mode: %w", err)
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

func (a *threadsAgent) listThreads(ctx context.Context, opt ThreadsAgentOptions) ([]sandbox.ThreadInfo, error) {
	log := klog.FromContext(ctx)

	var out []sandbox.ThreadInfo

	// gemini --list-sessions only does one directory (the cwd), and doesn't output structured information.

	// TODO: Fix identity in dev-sandbox (it will likely trigger some alarms if we keep using root)
	geminiDir := "/root/.gemini/tmp"

	if _, err := os.Stat(geminiDir); os.IsNotExist(err) {
		log.Info("No gemini sessions found (gemini tmp dir does not exist)", "dir", geminiDir)
		return out, nil
	}

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

		if opt.ThreadID != "" && session.SessionID != opt.ThreadID {
			return nil
		}

		thread := sandbox.ThreadInfo{
			SessionID:   session.SessionID,
			ProjectHash: session.ProjectHash,
		}
		if t, err := time.Parse(time.RFC3339, session.StartTime); err != nil {
			log.Error(err, "failed to parse start time", "startTime", session.StartTime, "path", path)
		} else {
			thread.StartTime = t
		}

		workspace, err := inferWorkspace(path)
		if err != nil {
			log.Error(err, "failed to infer workspace from gemini session file path", "path", path)
		}
		thread.Workspace = workspace

		// Compute token statistics
		for _, msg := range session.Messages {
			thread.TotalTokens += msg.Tokens.Total
		}

		if opt.IncludeMessages {
			for _, msg := range session.Messages {
				msgOut := sandbox.ThreadMessage{
					ID:        msg.ID,
					Timestamp: msg.Timestamp,
					Type:      msg.Type,
					Content:   msg.Content,
					Model:     msg.Model,
				}
				for _, toolCall := range msg.ToolCalls {
					toolCallOut := sandbox.ToolCall{
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

// inferWorkspace tries to infer the workspace directory from the gemini session file path.
// Annoyingly, this is not actually directly stored in the tmp directory, so we have to see if we can guess the workspace and generate a matching hash.
func inferWorkspace(geminiSessionFilePath string) (string, error) {
	// gemini session files are stored in /root/.gemini/tmp/<workspace-hash>/chats/session-<session-id>.json
	chatsDir := filepath.Dir(geminiSessionFilePath)
	if filepath.Base(chatsDir) != "chats" {
		return "", fmt.Errorf("unexpected gemini session file path: %q", geminiSessionFilePath)
	}
	workspaceDir := filepath.Dir(chatsDir)
	workspaceHash := filepath.Base(workspaceDir)

	// Check all the directories under /workspaces, which is where dev-sandbox workspaces are stored
	workspacesDir := "/workspaces"
	entries, err := os.ReadDir(workspacesDir)
	if err != nil {
		return "", fmt.Errorf("failed to read workspaces dir %q: %w", workspacesDir, err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		p := filepath.Join(workspacesDir, entry.Name())

		// Check if the hash of this directory matches the workspace hash
		hash := computeGeminiWorkspaceHash(p)
		if hash == workspaceHash {
			return p, nil
		}
	}

	return "", fmt.Errorf("workspace for %q not found", geminiSessionFilePath)
}

func computeGeminiWorkspaceHash(dir string) string {
	hasher := sha256.New()
	hasher.Write([]byte(dir))
	hash := hasher.Sum(nil)
	return hex.EncodeToString(hash)
}
