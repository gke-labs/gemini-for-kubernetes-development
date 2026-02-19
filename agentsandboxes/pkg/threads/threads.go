/*
Copyright 2026 The Gemini Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package threads

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

const RepoSandboxBinary = "/opt/repo-agent/repo-sandbox"

// Executor defines the interface for executing commands in a sandbox.
type Executor interface {
	Exec(opts ExecOptions) error
}

// ExecOptions holds options for executing a command.
type ExecOptions struct {
	Command []string
	Stdout  io.Writer
	Stderr  io.Writer
}

// ListThreads lists LLM threads in the sandbox.
func ListThreads(executor Executor) ([]ThreadInfo, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	opts := ExecOptions{
		Command: []string{RepoSandboxBinary, "threads", "agent"},
		Stdout:  &stdout,
		Stderr:  &stderr,
	}

	if err := executor.Exec(opts); err != nil {
		return nil, fmt.Errorf("failed to list threads: %w, stderr: %s", err, stderr.String())
	}

	var threads []ThreadInfo
	if err := json.Unmarshal(stdout.Bytes(), &threads); err != nil {
		return nil, fmt.Errorf("failed to parse threads output: %w", err)
	}
	return threads, nil
}

// GetThread gets a specific LLM thread in the sandbox.
func GetThread(executor Executor, threadID string, includeMessages bool) (*ThreadInfo, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	args := []string{RepoSandboxBinary, "threads", "agent", fmt.Sprintf("--thread-id=%s", threadID)}
	if includeMessages {
		args = append(args, "--include-messages=true")
	}

	opts := ExecOptions{
		Command: args,
		Stdout:  &stdout,
		Stderr:  &stderr,
	}

	if err := executor.Exec(opts); err != nil {
		return nil, fmt.Errorf("failed to get thread: %w, stderr: %s", err, stderr.String())
	}

	var threads []ThreadInfo
	if err := json.Unmarshal(stdout.Bytes(), &threads); err != nil {
		return nil, fmt.Errorf("failed to parse thread output: %w", err)
	}

	if len(threads) == 0 {
		return nil, fmt.Errorf("thread with ID %q not found", threadID)
	}

	return &threads[0], nil
}

// GetThreadMessages gets the messages for a specific LLM thread in the sandbox.
func GetThreadMessages(executor Executor, threadID string) ([]ThreadMessage, error) {
	thread, err := GetThread(executor, threadID, true)
	if err != nil {
		return nil, err
	}
	return thread.Messages, nil
}
