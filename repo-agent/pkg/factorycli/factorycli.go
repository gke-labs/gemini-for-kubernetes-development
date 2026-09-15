/*
Copyright 2026.

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

// Package factorycli invokes the factory CLI as child processes. Process
// invocation is the only repo-agent<->factory contract (no Go-level imports),
// matching how overseer consumes factory. Invocations run attached: factory's
// resilient task execution reattaches to an already-running in-sandbox task
// when the same command is re-run, so a controller restart only requires
// re-invoking the command on the next reconcile.
package factorycli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"k8s.io/klog/v2"
)

// Labels and annotations factory puts on the sandboxes it manages
// (see factory/pkg/sandbox). Part of the wire contract, not imported.
const (
	LabelManaged             = "factory.gemini.google.com/managed"
	AnnotationTaskState      = "sandbox.gemini.google.com/last-task-state"
	AnnotationTaskType       = "sandbox.gemini.google.com/last-task-type"
	AnnotationCompletionTime = "sandbox.gemini.google.com/completion-time"

	TaskStateRunning   = "Running"
	TaskStateCompleted = "Completed"
	TaskStateFailed    = "Failed"
)

// FixSandboxName returns the sandbox name `factory fix` uses for an issue
// (EnsureFixSandbox in factory/pkg/sandbox: fix-<repo>-<issueNumber>).
func FixSandboxName(repo string, issueNumber int) string {
	return fmt.Sprintf("fix-%s-%d", repo, issueNumber)
}

// FixOptions are the inputs for a `factory fix` invocation.
type FixOptions struct {
	// Namespace the task (and its sandbox) runs in; factory resolves the
	// task identity from the factory-user Secret in this namespace.
	Namespace string
	// IssueURL is the GitHub issue to fix.
	IssueURL string
	// Instruction is an optional custom prompt (factory --instruction).
	Instruction string
	// Image overrides the sandbox base image; must be factory-compatible.
	Image string
	// WorkspaceDiskSize overrides the workspace PVC size.
	WorkspaceDiskSize string
	// GithubToken authenticates factory's host-side GitHub reads.
	GithubToken string
	// Timeout bounds the child process; the in-sandbox task itself is not
	// killed on timeout (--abort-on-cancel=false) and is reattached to by
	// the next invocation.
	Timeout time.Duration
}

// Result records the outcome of a finished invocation.
type Result struct {
	Err        error
	Output     string
	FinishedAt time.Time
}

// Launcher is the controller-facing interface (faked in tests).
type Launcher interface {
	// StartFix launches `factory fix` for key unless one is already running.
	// Returns false if an invocation for key is already in flight.
	StartFix(key string, opts FixOptions) bool
	IsRunning(key string) bool
	// LastResult returns the outcome of the most recently finished
	// invocation for key, if any.
	LastResult(key string) (Result, bool)
}

// Runner is the real Launcher: it execs the factory binary bundled in the
// controller image, one single-flight child process per key.
type Runner struct {
	Binary string

	mu      sync.Mutex
	running map[string]struct{}
	results map[string]Result
}

func NewRunner() *Runner {
	binary := os.Getenv("FACTORY_BINARY")
	if binary == "" {
		binary = "factory"
	}
	return &Runner{
		Binary:  binary,
		running: make(map[string]struct{}),
		results: make(map[string]Result),
	}
}

func (r *Runner) StartFix(key string, opts FixOptions) bool {
	r.mu.Lock()
	if _, ok := r.running[key]; ok {
		r.mu.Unlock()
		return false
	}
	r.running[key] = struct{}{}
	r.mu.Unlock()

	go r.runFix(key, opts)
	return true
}

func (r *Runner) IsRunning(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.running[key]
	return ok
}

func (r *Runner) LastResult(key string) (Result, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	res, ok := r.results[key]
	return res, ok
}

func (r *Runner) runFix(key string, opts FixOptions) {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Hour
	}
	// Detached from any reconcile context: the invocation outlives the
	// reconcile that started it.
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	args := []string{
		"fix",
		"--url", opts.IssueURL,
		"--namespace", opts.Namespace,
		"--timeout", timeout.String(),
		// Never kill the in-sandbox task when this process dies; the next
		// invocation reattaches instead.
		"--abort-on-cancel=false",
	}
	if opts.Instruction != "" {
		args = append(args, "--instruction", opts.Instruction)
	}
	if opts.Image != "" {
		args = append(args, "--image", opts.Image)
	}
	if opts.WorkspaceDiskSize != "" {
		args = append(args, "--workspace-disk-size", opts.WorkspaceDiskSize)
	}

	cmd := exec.CommandContext(ctx, r.Binary, args...)
	cmd.Env = append(os.Environ(), "GITHUB_TOKEN="+opts.GithubToken)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	klog.Infof("factorycli: starting %s fix --url %s --namespace %s (key %s)", r.Binary, opts.IssueURL, opts.Namespace, key)
	err := cmd.Run()
	if err != nil {
		klog.Errorf("factorycli: fix for %s failed: %v\noutput tail:\n%s", key, err, tail(out.String(), 4096))
	} else {
		klog.Infof("factorycli: fix for %s completed", key)
	}

	r.mu.Lock()
	delete(r.running, key)
	r.results[key] = Result{Err: err, Output: out.String(), FinishedAt: time.Now()}
	r.mu.Unlock()
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
