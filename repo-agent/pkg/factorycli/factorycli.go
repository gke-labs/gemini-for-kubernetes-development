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
	"strings"
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

// LabelPR is the label factory puts on any sandbox working on a PR
// (EnsureReviewSandbox looks sandboxes up by it, so a review may land on a
// fix sandbox aliased to the same PR instead of the default factory-pr-<n>).
const LabelPR = "factory.gemini.google.com/pr"

// draftPostedMarker appears in `factory pr review --publish draft` output
// once the pending review has been posted to GitHub (draft mode prints no
// CODE REVIEW banner, so this line is the completion signal).
const draftPostedMarker = "Posting review as a draft (pending) review to GitHub PR"

// DraftWasPosted reports whether an invocation's output shows the review
// was published as a pending (draft) review on GitHub.
func DraftWasPosted(output string) bool {
	return strings.Contains(output, draftPostedMarker)
}

// ReviewOptions are the inputs for a `factory pr review` invocation.
type ReviewOptions struct {
	// SandboxName enables the in-flight preflight (factory-pr-<repo>-<n>).
	SandboxName string

	Namespace string
	PRURL     string
	// Instructions are passed as repeated --instruction flags (review
	// prompt plus any policy lines like severity thresholds).
	Instructions      []string
	Image             string
	WorkspaceDiskSize string
	GithubToken       string
	// Engine selects the agent engine (factory --engine); empty = gemini.
	Engine  string
	Timeout time.Duration
	// Publish is factory's --publish policy. "no" (default) prints the
	// review between CODE REVIEW banners for draft harvesting; "draft"
	// posts a pending review on GitHub under the invoking identity (only
	// visible to that identity — use the consenting member's token).
	Publish string
}

// PRWatchOptions are the inputs for a `factory pr watch` invocation, the
// follow-up loop for a PR the factory created: it investigates failing
// checks, addresses new review comments, and exits once the PR is merged or
// closed. The child is bounded by Timeout and simply relaunched by a later
// reconcile, so watching survives controller restarts.
type PRWatchOptions struct {
	Namespace   string
	PRURL       string
	GithubToken string
	// Engine selects the agent engine (factory --engine); empty = gemini.
	Engine  string
	Timeout time.Duration
}

// FixOptions are the inputs for a `factory fix` invocation.
type FixOptions struct {
	// SandboxName enables the in-flight preflight (fix-<repo>-<n>).
	SandboxName string

	// Namespace the task (and its sandbox) runs in; factory resolves the
	// task identity from the factory-user Secret in this namespace.
	Namespace string
	// IssueURL is the GitHub issue to fix.
	IssueURL string
	// Instruction is an optional custom prompt (factory --instruction).
	Instruction string
	// WithPlan folds the approved plan from a prior `factory plan` run
	// (living in the fix sandbox) into the fix prompt.
	WithPlan bool
	// Image overrides the sandbox base image; must be factory-compatible.
	Image string
	// WorkspaceDiskSize overrides the workspace PVC size.
	WorkspaceDiskSize string
	// GithubToken authenticates factory's host-side GitHub reads.
	GithubToken string
	// Engine selects the agent engine (factory --engine); empty = gemini.
	Engine string
	// Timeout bounds the child process; the in-sandbox task itself is not
	// killed on timeout (--abort-on-cancel=false) and is reattached to by
	// the next invocation.
	Timeout time.Duration
}

// PlanOptions are the inputs for a `factory plan` invocation: an
// implementation plan prepared in the issue's fix sandbox, printed between
// ISSUE PLAN banners (see ExtractPlan) and left in the sandbox for a later
// `factory fix --with-plan`. Nothing is written to GitHub.
type PlanOptions struct {
	// SandboxName enables the in-flight preflight (the issue fix sandbox).
	SandboxName string

	Namespace string
	IssueURL  string
	// Feedback revises the previous plan in the sandbox against maintainer
	// feedback instead of planning fresh.
	Feedback          string
	Image             string
	WorkspaceDiskSize string
	GithubToken       string
	// Engine selects the agent engine (factory --engine); empty = gemini.
	Engine  string
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
	// StartReview launches `factory pr review` for key unless
	// one is already running. The review YAML is recovered from the
	// invocation's output (see DraftWasPosted) via LastResult.
	StartReview(key string, opts ReviewOptions) bool
	// StartPRWatch launches `factory pr watch` for key unless one is
	// already running.
	StartPRWatch(key string, opts PRWatchOptions) bool
	// StartPlan launches `factory plan` for key unless one is already
	// running; a controller pass harvests the finished invocation's output
	// (see ExtractPlan) via LastResult.
	StartPlan(key string, opts PlanOptions) bool
	// StartTriage launches `factory triage --publish no` for key unless
	// one is already running. The triage YAML is recovered from the
	// invocation's output (see ExtractTriageYAML) via LastResult.
	StartTriage(key string, opts TriageOptions) bool
	IsRunning(key string) bool
	// LastResult returns the outcome of the most recently finished
	// invocation for key, if any.
	LastResult(key string) (Result, bool)
}

// Runner is the real Launcher: it execs the factory binary bundled in the
// controller image, one single-flight child process per key.
type Runner struct {
	Prober TaskProber

	Binary string

	mu      sync.Mutex
	running map[string]struct{}
	results map[string]Result
}

// TaskProber is the dispatchTask discipline from factory's watch
// dispatcher, applied to the runner: before spawning an invocation, probe
// the target sandbox. A busy sandbox is SKIPPED (the reconcile loop is
// the requeue); an orphaned finished task — the annotation still claims
// Running because the invocation that launched it died with the old
// controller — is ADOPTED (its output becomes the result; nothing
// re-executes) or, for task types with host-side completion steps,
// corrected and released for a normal launch. Nil disables preflight.
type TaskProber interface {
	// Probe inspects the newest <prefix>-* task in the sandbox against
	// the sandbox's recorded task state, correcting stale annotations as
	// a side effect (the watch IsTaskRunning discipline).
	Probe(ctx context.Context, namespace, sandboxName, prefix, outputFile string) (TaskProbe, error)
}

// TaskProbe is a probe verdict.
type TaskProbe struct {
	// State is one of "none" (nothing in flight — launch), "running"
	// (skip; the next reconcile retries), "orphan-completed" (a finished
	// run nobody harvested: annotation claimed Running, exit code
	// present).
	State    string
	ExitCode string
	// Output is the collected output file content (orphan-completed with
	// a requested outputFile only).
	Output string
}

const (
	ProbeNone            = "none"
	ProbeRunning         = "running"
	ProbeOrphanCompleted = "orphan-completed"
)

// preflight describes the probe for one invocation. When outputFile is
// set the task type is adoptable (its harvest contract is a file: plan,
// triage); review and fix finish host-side, so their orphans are
// corrected and relaunched normally.
type preflight struct {
	namespace  string
	sandbox    string
	prefix     string
	outputFile string
	banner     string
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
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Hour
	}
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
	if opts.WithPlan {
		args = append(args, "--with-plan")
	}
	if opts.Image != "" {
		args = append(args, "--image", opts.Image)
	}
	if opts.WorkspaceDiskSize != "" {
		args = append(args, "--workspace-disk-size", opts.WorkspaceDiskSize)
	}
	if opts.Engine != "" {
		args = append(args, "--engine", opts.Engine)
	}
	return r.startWithPreflight(key, args, opts.GithubToken, timeout, &preflight{
		namespace: opts.Namespace, sandbox: opts.SandboxName, prefix: "fix",
	})
}

func (r *Runner) StartReview(key string, opts ReviewOptions) bool {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 45 * time.Minute
	}
	publish := opts.Publish
	if publish == "" {
		publish = "no"
	}
	args := []string{
		"pr", "review",
		"--pr-url", opts.PRURL,
		"--publish", publish,
		"--namespace", opts.Namespace,
		"--timeout", timeout.String(),
		"--abort-on-cancel=false",
	}
	for _, instruction := range opts.Instructions {
		if instruction != "" {
			args = append(args, "--instruction", instruction)
		}
	}
	if opts.Image != "" {
		args = append(args, "--image", opts.Image)
	}
	if opts.WorkspaceDiskSize != "" {
		args = append(args, "--workspace-disk-size", opts.WorkspaceDiskSize)
	}
	if opts.Engine != "" {
		args = append(args, "--engine", opts.Engine)
	}
	return r.startWithPreflight(key, args, opts.GithubToken, timeout, &preflight{
		namespace: opts.Namespace, sandbox: opts.SandboxName, prefix: "review",
	})
}

func (r *Runner) StartPRWatch(key string, opts PRWatchOptions) bool {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 55 * time.Minute
	}
	// Let the watch loop exit on its own before the hard process timeout.
	watchTimeout := timeout - 5*time.Minute
	if watchTimeout <= 0 {
		watchTimeout = timeout / 2
	}
	args := []string{
		"pr", "watch",
		"--pr-url", opts.PRURL,
		"--namespace", opts.Namespace,
		"--watch-timeout", watchTimeout.String(),
		"--timeout", timeout.String(),
		"--continue-session",
		"--abort-on-cancel=false",
	}
	if opts.Engine != "" {
		args = append(args, "--engine", opts.Engine)
	}
	return r.start(key, args, opts.GithubToken, timeout)
}

func (r *Runner) StartTriage(key string, opts TriageOptions) bool {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	args := []string{
		"triage",
		"--url", opts.IssueURL,
		"--publish", "no",
		"--namespace", opts.Namespace,
		"--timeout", timeout.String(),
		"--abort-on-cancel=false",
	}
	if opts.Engine != "" {
		args = append(args, "--engine", opts.Engine)
	}
	return r.startWithPreflight(key, args, opts.GithubToken, timeout, &preflight{
		namespace: opts.Namespace, sandbox: opts.SandboxName, prefix: "triage",
		outputFile: "triage-output.txt", banner: triageBanner,
	})
}

func (r *Runner) StartPlan(key string, opts PlanOptions) bool {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	args := []string{
		"plan",
		"--url", opts.IssueURL,
		"--namespace", opts.Namespace,
		"--timeout", timeout.String(),
		"--abort-on-cancel=false",
	}
	if opts.Feedback != "" {
		args = append(args, "--feedback", opts.Feedback)
	}
	if opts.Image != "" {
		args = append(args, "--image", opts.Image)
	}
	if opts.WorkspaceDiskSize != "" {
		args = append(args, "--workspace-disk-size", opts.WorkspaceDiskSize)
	}
	if opts.Engine != "" {
		args = append(args, "--engine", opts.Engine)
	}
	return r.startWithPreflight(key, args, opts.GithubToken, timeout, &preflight{
		namespace: opts.Namespace, sandbox: opts.SandboxName, prefix: "plan",
		outputFile: "plan-output.txt", banner: planBanner,
	})
}

func (r *Runner) start(key string, args []string, githubToken string, timeout time.Duration) bool {
	return r.startWithPreflight(key, args, githubToken, timeout, nil)
}

func (r *Runner) startWithPreflight(key string, args []string, githubToken string, timeout time.Duration, pre *preflight) bool {
	r.mu.Lock()
	if _, ok := r.running[key]; ok {
		r.mu.Unlock()
		return false
	}
	r.mu.Unlock()

	// dispatchTask discipline, before taking the slot: busy sandbox →
	// skip (the next reconcile is the requeue); orphaned finished task →
	// adopt its output as the result instead of re-executing.
	if pre != nil && r.Prober != nil && pre.sandbox != "" {
		probeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		probe, err := r.Prober.Probe(probeCtx, pre.namespace, pre.sandbox, pre.prefix, pre.outputFile)
		cancel()
		if err == nil {
			switch probe.State {
			case ProbeRunning:
				klog.Infof("factorycli: sandbox %s/%s busy with an in-flight %s task; skipping launch (key %s)", pre.namespace, pre.sandbox, pre.prefix, key)
				return false
			case ProbeOrphanCompleted:
				if pre.outputFile != "" {
					klog.Infof("factorycli: adopting orphaned %s result in %s/%s (key %s)", pre.prefix, pre.namespace, pre.sandbox, key)
					res := Result{FinishedAt: time.Now()}
					if probe.ExitCode == "0" {
						res.Output = pre.banner + "\n" + probe.Output + "\n================================================\n"
					} else {
						res.Err = fmt.Errorf("adopted %s task exited %s", pre.prefix, probe.ExitCode)
						res.Output = probe.Output
					}
					r.mu.Lock()
					r.results[key] = res
					r.mu.Unlock()
					return true
				}
				// Host-side task types: the annotation correction already
				// happened in Probe; fall through to a normal launch
				// against the settled sandbox.
			}
		}
	}

	r.mu.Lock()
	if _, ok := r.running[key]; ok {
		r.mu.Unlock()
		return false
	}
	r.running[key] = struct{}{}
	r.mu.Unlock()

	go r.run(key, args, githubToken, timeout)
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

func (r *Runner) run(key string, args []string, githubToken string, timeout time.Duration) {
	// Detached from any reconcile context: the invocation outlives the
	// reconcile that started it.
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, r.Binary, args...)
	cmd.Env = append(os.Environ(), "GITHUB_TOKEN="+githubToken)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	klog.Infof("factorycli: starting %s %s (key %s)", r.Binary, strings.Join(args[:2], " "), key)
	err := cmd.Run()
	if err != nil {
		klog.Errorf("factorycli: %s for %s failed: %v\noutput tail:\n%s", args[0], key, err, tail(out.String(), 4096))
	} else {
		klog.Infof("factorycli: %s for %s completed", args[0], key)
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

// TriageOptions are the inputs for a `factory triage` invocation
// (draft-only issue triage; --publish no writes nothing to GitHub).
type TriageOptions struct {
	// SandboxName enables the in-flight preflight (triage-<repo>-<n>).
	SandboxName string

	Namespace   string
	IssueURL    string
	GithubToken string
	// Engine selects the agent engine (factory --engine); empty = gemini.
	Engine  string
	Timeout time.Duration
}

// triageBanner opens the triage YAML on `factory triage --publish no`
// stdout (factory/pkg/commands/triage.go).
const triageBanner = "================= ISSUE TRIAGE ================="

// ExtractTriageYAML returns the triage YAML printed after the ISSUE TRIAGE
// banner of a completed `factory triage` invocation, or "".
func ExtractTriageYAML(output string) string {
	start := strings.Index(output, triageBanner)
	if start < 0 {
		return ""
	}
	rest := output[start+len(triageBanner):]
	if end := strings.Index(rest, "================"); end >= 0 {
		rest = rest[:end]
	}
	return strings.TrimSpace(rest)
}

// planBanner opens the plan text on `factory plan` output; the closer is
// any run of at least sixteen '=' characters.
const planBanner = "================== ISSUE PLAN =================="

// ExtractPlan extracts the plan markdown from a `factory plan` invocation's
// output; empty when no plan was produced.
func ExtractPlan(output string) string {
	start := strings.Index(output, planBanner)
	if start < 0 {
		return ""
	}
	rest := output[start+len(planBanner):]
	if end := strings.Index(rest, "================"); end >= 0 {
		rest = rest[:end]
	}
	return strings.TrimSpace(rest)
}

// TriageSandboxName returns the sandbox name `factory triage` uses
// (EnsureTriageSandbox: triage-<repo>-<issueNumber>).
func TriageSandboxName(repo string, issueNumber int) string {
	return fmt.Sprintf("triage-%s-%d", repo, issueNumber)
}
