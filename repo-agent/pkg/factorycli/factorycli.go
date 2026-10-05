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
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
	"k8s.io/klog/v2"
)

// Labels and annotations factory puts on the sandboxes it manages
// (see factory/pkg/sandbox). Part of the wire contract, not imported.
const (
	LabelManaged             = "factory.gemini.google.com/managed"
	LabelLauncher            = "factory.gemini.google.com/launcher"
	AnnotationTaskState      = "sandbox.gemini.google.com/last-task-state"
	AnnotationTaskType       = "sandbox.gemini.google.com/last-task-type"
	AnnotationCompletionTime = "sandbox.gemini.google.com/completion-time"

	TaskStateRunning   = "Running"
	TaskStateCompleted = "Completed"
	TaskStateFailed    = "Failed"
)

// LauncherName is what repo-agent passes factory as --launcher, which factory
// records on the sandboxes it creates as LabelLauncher.
const LauncherName = "repo-agent"

// LaunchedElsewhere reports whether another launcher — the factory CLI run
// by hand, say — created the sandbox. The board leaves those alone: a
// sandbox `factory recipe triage` was run in by hand is not one of its
// triages to resume, publish or pause. A sandbox without the label
// predates it and is treated as the board's, as it always was.
func LaunchedElsewhere(labels map[string]string) bool {
	l := labels[LabelLauncher]
	return l != "" && l != LauncherName
}

// RunTaskPrefix is the task directory prefix `factory run` writes:
// /workspaces/tasks/run-<ts>/. Everything that looks for a run's
// in-flight work — the launch preflight, the Runs list's liveness probe
// — reads it from here, so a rename cannot leave one of them looking
// under a name nothing writes any more.
const RunTaskPrefix = "run"

// FixSandboxName returns the sandbox name `factory fix` uses for an issue
// (EnsureFixSandbox in factory/pkg/sandbox: fix-<repo>-<issueNumber>).
func FixSandboxName(repo string, issueNumber int) string {
	return fmt.Sprintf("fix-%s-%d", repo, issueNumber)
}

// RunbookInstance is the default instance name for a runbook —
// mirrors factory's default so claims and sandbox names agree.
func RunbookInstance(runbook, instance string) string {
	if instance != "" {
		return instance
	}
	return runbook
}

// RunbookSandboxName mirrors factory's run-environment naming:
// runbook-<repo>-<instance>, budgeted for the -lb DNS cap.
func RunbookSandboxName(repo, instance string) string {
	slugify := func(s string) string {
		s = strings.ToLower(s)
		var b strings.Builder
		for _, r := range s {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
				b.WriteRune(r)
			} else {
				b.WriteRune('-')
			}
		}
		return strings.Trim(b.String(), "-")
	}
	suffix := slugify(instance)
	slug := slugify(repo)
	if budget := 60 - len("runbook-") - len(suffix) - 1; len(slug) > budget {
		slug = strings.Trim(slug[:budget], "-")
	}
	return "runbook-" + slug + "-" + suffix
}

// RunOptions parameterize `factory run <mode>` — planning, deploying
// or tearing down one run, in that run's own sandbox.
type RunOptions struct {
	SandboxName string
	Namespace   string
	// Mode is plan | deploy | teardown.
	Mode string
	// Name is the run's identity and its directory under
	// docs-exploration/agent-runs/. It is the old instance name: adoption of
	// a legacy deployment matches on it, and so does the sandbox.
	Name string
	// Intent is the owner's free text — the brief on a first plan, the
	// amendment on a re-plan.
	Intent string
	// Runbook is what a new run is copied from before it is planned:
	// .agents/runbooks/<name> in the repository, or another run.
	Runbook string
	// Target is the pull request a plan pins this run to; 0 is the
	// default branch.
	Target      int
	RepoURL     string
	GithubToken string
	Timeout     time.Duration
	Engine      string
}

// StartRun launches `factory run <mode>`.
func (r *Runner) StartRun(key string, opts RunOptions) bool {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Minute
	}
	args := []string{
		"run", opts.Mode,
		"--url", opts.RepoURL,
		"--namespace", opts.Namespace,
		"--timeout", timeout.String(),
		"--abort-on-cancel=false",
		"--name", opts.Name,
	}
	if opts.Intent != "" && opts.Mode != "deploy" {
		// Deploy executes a plan the owner already reviewed; taking
		// fresh instructions there would change what was approved.
		args = append(args, "--intent", opts.Intent)
	}
	if opts.Runbook != "" && opts.Mode == "plan" {
		// Only a plan starts from a runbook; factory refuses it anywhere
		// else, and a refusal there would strand the click.
		args = append(args, "--runbook", opts.Runbook)
	}
	if opts.Target > 0 && opts.Mode == "plan" {
		// Plan only, for the same reason: deploy and teardown execute
		// the commit the plan pinned.
		args = append(args, "--target", strconv.Itoa(opts.Target))
	}
	if opts.Engine != "" {
		args = append(args, "--engine", opts.Engine)
	}
	return r.startWithPreflight(key, args, opts.GithubToken, timeout, &preflight{
		// A stale prefix here would make the preflight look for
		// in-flight work under a name nothing writes any more, and every
		// launch would think the sandbox was free.
		namespace: opts.Namespace, sandbox: opts.SandboxName, prefix: RunTaskPrefix,
	})
}

// PRSandboxName mirrors factory's ReviewSandboxName (factory-pr-<slug>-<n>,
// slug lowercased/dashed, budgeted for the -lb Service's DNS label cap) —
// the sandbox `factory pr <verb>` creates when a PR has none.
func PRSandboxName(repo string, prNum int) string {
	slug := strings.ToLower(repo)
	var b strings.Builder
	for _, r := range slug {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	slug = strings.Trim(b.String(), "-")
	suffix := fmt.Sprintf("-%d", prNum)
	if budget := 60 - len("factory-pr-") - len(suffix); len(slug) > budget {
		slug = strings.Trim(slug[:budget], "-")
	}
	return "factory-pr-" + slug + suffix
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
	// Disclose is factory's --disclose: whether what the agent posts says
	// an agent wrote it. Always passed, because factory defaults it on.
	Disclose bool
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
	// WithPlan folds the approved plan from a prior `factory recipe plan` run
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
	// Disclose is factory's --disclose: whether what the agent posts says
	// an agent wrote it. Always passed, because factory defaults it on.
	Disclose bool
}

// PlanOptions are the inputs for a `factory recipe plan` invocation: an
// implementation plan prepared in the issue's sandbox, read back as a Plan
// task output (see ExtractPlan) and left in the sandbox for a later
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
	// RunName records the plan's task in the sandbox, to read its result
	// back by (factory --run-name).
	RunName string
}

// PRTaskOptions are the inputs for the follow-up verbs on a factory PR:
// `pr investigate` (CI failures), `pr address-comments` (review feedback),
// `pr iterate` (free-form instruction / merge conflicts). All three run in
// the PR's fix sandbox, push to the PR branch under the invoking identity,
// and continue the fix conversation (--continue-session in the scripts).
type PRTaskOptions struct {
	// SandboxName enables the in-flight preflight (fix-<repo>-<n>).
	SandboxName string

	Namespace string
	PRURL     string
	// Instruction overrides factory's default prompt for the task; empty
	// keeps the default ("Resolve merge conflicts and iterate", ...).
	Instruction string
	GithubToken string
	Timeout     time.Duration
	// Engine selects the agent engine (factory --engine); empty = gemini.
	Engine string
	// Disclose is factory's --disclose: whether what the agent posts says
	// an agent wrote it. Always passed, because factory defaults it on.
	Disclose bool
}

func (r *Runner) startPRTask(key, subcommand, prefix string, opts PRTaskOptions) bool {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 45 * time.Minute
	}
	args := []string{
		"pr", subcommand,
		"--pr-url", opts.PRURL,
		"--namespace", opts.Namespace,
		"--timeout", timeout.String(),
		"--abort-on-cancel=false",
	}
	if opts.Instruction != "" {
		args = append(args, "--prompt", opts.Instruction)
	}
	if opts.Engine != "" {
		args = append(args, "--engine", opts.Engine)
	}
	args = append(args, "--disclose="+strconv.FormatBool(opts.Disclose))
	return r.startWithPreflight(key, args, opts.GithubToken, timeout, &preflight{
		namespace: opts.Namespace, sandbox: opts.SandboxName, prefix: prefix,
	})
}

// StartInvestigate launches `factory pr investigate` (CI check failures).
func (r *Runner) StartInvestigate(key string, opts PRTaskOptions) bool {
	return r.startPRTask(key, "investigate", "investigate", opts)
}

// StartAddressComments launches `factory pr address-comments`.
func (r *Runner) StartAddressComments(key string, opts PRTaskOptions) bool {
	return r.startPRTask(key, "address-comments", "address", opts)
}

// StartIterate launches `factory pr iterate`.
func (r *Runner) StartIterate(key string, opts PRTaskOptions) bool {
	return r.startPRTask(key, "iterate", "iterate", opts)
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
	// StartPlan launches `factory recipe plan` for key unless one is already
	// running; a controller pass harvests the finished invocation's output
	// (see ExtractPlan) via LastResult.
	StartPlan(key string, opts PlanOptions) bool
	// StartRevise launches `factory recipe revise` for key unless one is
	// already running; harvested as a plan is.
	StartRevise(key string, opts ReviseOptions) bool
	// StartRun launches `factory run <mode>` (plan, deploy or teardown
	// of one run, in that run's sandbox) unless one is already running.
	StartRun(key string, opts RunOptions) bool
	// StartInvestigate / StartAddressComments / StartIterate launch the
	// PR follow-up verbs in the PR's fix sandbox.
	StartInvestigate(key string, opts PRTaskOptions) bool
	StartAddressComments(key string, opts PRTaskOptions) bool
	StartIterate(key string, opts PRTaskOptions) bool
	// StartTriage launches `factory recipe triage` for key unless one is
	// already running. The triage YAML is recovered from the result's
	// output (see ExtractTriageYAML) via LastResult.
	StartTriage(key string, opts TriageOptions) bool
	// StartResearch launches `factory recipe research`, detached: the
	// sandbox one deep-research conversation runs in, and its start (the
	// clone and the opening question), whose session the conversation is.
	StartResearch(key string, opts ResearchOptions) bool
	// StartApply launches `factory apply --action`: one write of a draft's
	// task output to its issue, with the member's token.
	StartApply(key string, opts ApplyOptions) bool
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
// controller — has its annotation corrected and launches normally, its
// task type finishing host-side (fix, review). Recipe runs (triage, plan)
// are not probed: they are resumed by run name. Nil disables preflight.
type TaskProber interface {
	// Probe inspects the newest <taskType>-* task in the sandbox against
	// the sandbox's recorded task state for taskType, correcting stale
	// annotations as a side effect (the watch IsTaskRunning discipline).
	Probe(ctx context.Context, namespace, sandboxName, taskType string) (TaskProbe, error)
}

// TaskProbe is a probe verdict.
type TaskProbe struct {
	// State is one of "none" (nothing in flight — launch), "running"
	// (skip; the next reconcile retries), "orphan-completed" (a finished
	// run nobody harvested: annotation claimed Running, exit code
	// present).
	State    string
	ExitCode string
}

const (
	ProbeNone            = "none"
	ProbeRunning         = "running"
	ProbeOrphanCompleted = "orphan-completed"
)

// preflight describes the probe for one invocation, and its harvest.
type preflight struct {
	namespace string
	sandbox   string
	prefix    string
	// harvest, when set, turns the invocation's output and error into the
	// result's, running more factory commands if it needs to.
	harvest func(ctx context.Context, out string, err error) (string, error)
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
	args = append(args, "--disclose="+strconv.FormatBool(opts.Disclose))
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
	args = append(args, "--disclose="+strconv.FormatBool(opts.Disclose))
	return r.start(key, args, opts.GithubToken, timeout)
}

// StartTriage runs `factory recipe triage` and reads its result back with
// `factory sandbox task output --run-name`, as a typed Triage task
// output. The result's Output has it between triageBanner and a closer,
// for ExtractTriageYAML.
func (r *Runner) StartTriage(key string, opts TriageOptions) bool {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	args := []string{
		"recipe", "triage",
		"--run-name", opts.RunName,
		"--url", opts.IssueURL,
		"--namespace", opts.Namespace,
		"--timeout", timeout.String(),
		"--abort-on-cancel=false",
	}
	if opts.Engine != "" {
		args = append(args, "--engine", opts.Engine)
	}
	sandbox := opts.SandboxName
	if sandbox == "" {
		sandbox = opts.IssueURL
	}
	// No probe: the run name is the run. Invoked again with it, factory
	// follows the run or reads its result, and refuses a busy sandbox.
	return r.startWithPreflight(key, args, opts.GithubToken, timeout, &preflight{
		harvest: func(ctx context.Context, out string, err error) (string, error) {
			if err != nil {
				return out, err
			}
			doc, err := r.execStdout(ctx, []string{"sandbox", "task", "output", sandbox, "--namespace", opts.Namespace, "--run-name", opts.RunName}, opts.GithubToken)
			if err != nil {
				return out + "\n" + doc, fmt.Errorf("reading the triage's task output: %w", err)
			}
			return triageBanner + "\n" + doc + "\n" + bannerCloser + "\n", nil
		},
	})
}

// StartPlan runs `factory recipe plan` and reads its result back with
// `factory sandbox task output --run-name`, as a typed Plan task output.
// The result's Output has it between planBanner and a closer, for
// ExtractPlan. The recipe is the sandbox's main task, of type plan, so
// last-task-type stays plan.
func (r *Runner) StartPlan(key string, opts PlanOptions) bool {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	args := []string{
		"recipe", "plan",
		"--run-name", opts.RunName,
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
	sandbox := opts.SandboxName
	if sandbox == "" {
		sandbox = opts.IssueURL
	}
	// No probe, as for triage.
	return r.startWithPreflight(key, args, opts.GithubToken, timeout, r.planHarvest(sandbox, opts.Namespace, opts.RunName, opts.GithubToken))
}

// planHarvest reads a finished plan run's Plan task output back by its run
// name, between planBanner and a closer, for ExtractPlan.
func (r *Runner) planHarvest(sandbox, namespace, runName, githubToken string) *preflight {
	return &preflight{
		harvest: func(ctx context.Context, out string, err error) (string, error) {
			if err != nil {
				return out, err
			}
			doc, err := r.execStdout(ctx, []string{"sandbox", "task", "output", sandbox, "--namespace", namespace, "--run-name", runName}, githubToken)
			if err != nil {
				return out + "\n" + doc, fmt.Errorf("reading the plan's task output: %w", err)
			}
			return planBanner + "\n" + doc + "\n" + bannerCloser + "\n", nil
		},
	}
}

// ReviseOptions are the inputs for a `factory recipe revise` invocation:
// one of a recipe's revises, asked into the agent session its task ran
// in, as the next turn of the conversation a member continued there — a
// plan's Update plan (a Plan task output, as a plan's) or a research
// conversation's Save notes (a Notes task output). Nothing is written to
// GitHub.
type ReviseOptions struct {
	// SandboxName is the sandbox the task ran in.
	SandboxName string
	Namespace   string
	// Revise is the revise's id in the recipe ("plan", "notes").
	Revise string
	// Session is the task whose session to revise in; empty lets factory
	// pick the sandbox's newest task with the revise.
	Session     string
	GithubToken string
	Timeout     time.Duration
	// RunName records the revise's task in the sandbox, to read its
	// result back by.
	RunName string
}

// StartRevise runs `factory recipe revise` and reads its result back as
// StartPlan does, after planBanner: for ExtractPlan, or ExtractNotes. The
// revise runs on the sandbox's disk and engine as its task left them, so
// it takes no image, disk or engine.
func (r *Runner) StartRevise(key string, opts ReviseOptions) bool {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Minute
	}
	args := []string{
		"recipe", "revise", opts.SandboxName, opts.Revise,
		"--run-name", opts.RunName,
		"--namespace", opts.Namespace,
		"--timeout", timeout.String(),
		"--abort-on-cancel=false",
	}
	if opts.Session != "" {
		args = append(args, "--task", opts.Session)
	}
	return r.startWithPreflight(key, args, opts.GithubToken, timeout, r.planHarvest(opts.SandboxName, opts.Namespace, opts.RunName, opts.GithubToken))
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
	// skip (the next reconcile is the requeue). An orphaned finished task
	// has had its annotation corrected by Probe and launches normally.
	if pre != nil && r.Prober != nil && pre.sandbox != "" {
		probeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		probe, err := r.Prober.Probe(probeCtx, pre.namespace, pre.sandbox, pre.prefix)
		cancel()
		if err == nil && probe.State == ProbeRunning {
			klog.Infof("factorycli: sandbox %s/%s busy with an in-flight %s task; skipping launch (key %s)", pre.namespace, pre.sandbox, pre.prefix, key)
			return false
		}
	}

	r.mu.Lock()
	if _, ok := r.running[key]; ok {
		r.mu.Unlock()
		return false
	}
	r.running[key] = struct{}{}
	r.mu.Unlock()

	go r.run(key, args, githubToken, timeout, pre)
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

func (r *Runner) run(key string, args []string, githubToken string, timeout time.Duration, pre *preflight) {
	// Detached from any reconcile context: the invocation outlives the
	// reconcile that started it.
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	klog.Infof("factorycli: starting %s %s (key %s)", r.Binary, strings.Join(args[:2], " "), key)
	out, err := r.exec(ctx, args, githubToken)
	if pre != nil && pre.harvest != nil {
		out, err = pre.harvest(ctx, out, err)
	}
	if err != nil {
		klog.Errorf("factorycli: %s for %s failed: %v\noutput tail:\n%s", args[0], key, err, tail(out, 4096))
	} else {
		klog.Infof("factorycli: %s for %s completed", args[0], key)
	}

	r.mu.Lock()
	delete(r.running, key)
	r.results[key] = Result{Err: err, Output: out, FinishedAt: time.Now()}
	r.mu.Unlock()
}

// exec runs one factory command to its end and returns what it printed,
// stdout and stderr together.
func (r *Runner) exec(ctx context.Context, args []string, githubToken string) (string, error) {
	var out bytes.Buffer
	err := r.command(ctx, args, githubToken, &out, &out).Run()
	return out.String(), err
}

// execStdout runs one factory command to its end and returns its stdout:
// a document to parse, without the progress factory writes to stderr
// (waking the sandbox, connecting to it). On failure the stderr follows,
// for the error's output.
func (r *Runner) execStdout(ctx context.Context, args []string, githubToken string) (string, error) {
	var stdout, stderr bytes.Buffer
	if err := r.command(ctx, args, githubToken, &stdout, &stderr).Run(); err != nil {
		return stdout.String() + stderr.String(), err
	}
	return stdout.String(), nil
}

func (r *Runner) command(ctx context.Context, args []string, githubToken string, stdout, stderr io.Writer) *exec.Cmd {
	args = append(args[:len(args):len(args)], "--launcher="+LauncherName)
	cmd := exec.CommandContext(ctx, r.Binary, args...)
	cmd.Env = append(os.Environ(), "GITHUB_TOKEN="+githubToken)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// TriageOptions are the inputs for a `factory recipe triage` invocation
// (draft-only issue triage; it writes nothing to GitHub).
type TriageOptions struct {
	// SandboxName enables the in-flight preflight (the issue's sandbox).
	SandboxName string

	Namespace   string
	IssueURL    string
	GithubToken string
	// Engine selects the agent engine (factory --engine); empty = gemini.
	Engine  string
	Timeout time.Duration
	// RunName records the triage's task in the sandbox, to read its
	// result back by (factory --run-name).
	RunName string
}

// triageBanner opens the triage YAML in a triage's Output: StartTriage
// puts the recipe triage's result between it and bannerCloser.
const triageBanner = "================= ISSUE TRIAGE ================="

// bannerCloser closes what a banner opens.
const bannerCloser = "================================================"

// TaskOutputFile is where a recipe task leaves its typed result
// (factory/pkg/taskoutput).
const TaskOutputFile = "task-output.yaml"

// ExtractTriageYAML returns the triage YAML after the ISSUE TRIAGE banner
// of a completed triage, or "": the agent's `triage:` block as `factory
// triage` prints it, or the same made from a Triage task output.
func ExtractTriageYAML(output string) string {
	rest, ok := triageSection(output)
	if !ok {
		return ""
	}
	return NormalizeTriageDraft(rest)
}

// TriageTaskOutput is the Triage task output after the ISSUE TRIAGE banner
// of a completed triage without its spec, or "": kept for the actions it
// offers (OfferedActions) and its source; the draft is the spec.
func TriageTaskOutput(output string) string {
	rest, ok := triageSection(output)
	if !ok {
		return ""
	}
	if i := strings.Index(rest, "\napiVersion:"); !strings.HasPrefix(rest, "apiVersion:") && i >= 0 {
		rest = rest[i+1:]
	}
	if triageFromTaskOutput(rest) == "" {
		return ""
	}
	return withoutSpec(rest)
}

func triageSection(output string) (string, bool) {
	start := strings.Index(output, triageBanner)
	if start < 0 {
		return "", false
	}
	rest := output[start+len(triageBanner):]
	if end := strings.Index(rest, "================"); end >= 0 {
		rest = rest[:end]
	}
	return strings.TrimSpace(rest), true
}

// triageFromTaskOutput is a Triage task output's spec as the `triage:`
// block drafts are kept in, or "".
func triageFromTaskOutput(doc string) string {
	var d struct {
		Kind string    `yaml:"kind"`
		Spec yaml.Node `yaml:"spec"`
	}
	if err := yaml.Unmarshal([]byte(doc), &d); err != nil || d.Kind != "Triage" || d.Spec.Kind == 0 {
		return ""
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(map[string]*yaml.Node{"triage": &d.Spec}); err != nil {
		return ""
	}
	_ = enc.Close()
	return strings.TrimSpace(buf.String())
}

// planBanner opens the plan in a plan's Output: StartPlan puts the recipe
// plan's result between it and bannerCloser.
const planBanner = "================== ISSUE PLAN =================="

// ExtractPlan returns the plan markdown after the ISSUE PLAN banner of a
// completed plan, or "": a Plan task output's.
func ExtractPlan(output string) string {
	return planFromTaskOutput(planSection(output))
}

// PlanTaskOutput is the Plan task output after the ISSUE PLAN banner of a
// completed plan without its spec, or "": kept for the actions it offers
// (OfferedActions) and its source; the draft is the spec.
func PlanTaskOutput(output string) string {
	rest := planSection(output)
	if planFromTaskOutput(rest) == "" {
		return ""
	}
	return withoutSpec(rest)
}

func planSection(output string) string {
	start := strings.Index(output, planBanner)
	if start < 0 {
		return ""
	}
	rest := output[start+len(planBanner):]
	// The markdown may underline a heading with '='s: only the last
	// closer is the banner's.
	if end := strings.LastIndex(rest, bannerCloser); end >= 0 {
		rest = rest[:end]
	}
	return strings.TrimSpace(rest)
}

// planFromTaskOutput is a Plan task output's markdown, or "".
func planFromTaskOutput(doc string) string { return markdownFromTaskOutput("Plan", doc) }

// ExtractNotes returns the notes markdown of a completed Save notes revise
// (a research recipe's), or "": a Notes task output's. StartRevise puts
// any revise's result after planBanner.
func ExtractNotes(output string) string {
	return markdownFromTaskOutput("Notes", planSection(output))
}

// NotesTaskOutput is the Notes task output of a completed Save notes
// revise without its spec, or "", as PlanTaskOutput is a plan's.
func NotesTaskOutput(output string) string {
	rest := planSection(output)
	if markdownFromTaskOutput("Notes", rest) == "" {
		return ""
	}
	return withoutSpec(rest)
}

// markdownFromTaskOutput is the spec.markdown of a task output of kind,
// or "".
func markdownFromTaskOutput(kind, doc string) string {
	var d struct {
		Kind string `yaml:"kind"`
		Spec struct {
			Markdown string `yaml:"markdown"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal([]byte(doc), &d); err != nil || d.Kind != kind {
		return ""
	}
	return strings.TrimSpace(d.Spec.Markdown)
}
