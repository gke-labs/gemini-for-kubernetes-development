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
	"maps"
	"os"
	"os/exec"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/klog/v2"

	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
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

// LabelPR is the label factory puts on any sandbox working on a PR: a fix
// sandbox aliased to the PR its fix opened, or made for a PR (PRFixSandbox).
// EnsureReviewSandbox looks sandboxes up by it, so a review may land on a
// fix sandbox aliased to the same PR instead of the default factory-pr-<n>.
const LabelPR = "factory.gemini.google.com/pr"

// ReviewSandboxName is the sandbox `factory recipe review` runs a PR's
// review in: factory's RecipeSandboxName for the review recipe, which has
// a sandbox of its own (credentials: clone), review-<repo>-<n>. It carries
// no PR label, so `factory pr` never adopts it, and the board finds it by
// its name (ReviewPROf).
func ReviewSandboxName(repo string, prNum int) string {
	return fmt.Sprintf("review-%s-%d", repo, prNum)
}

// ReviewPROf is the PR the review sandbox sb is for, false for any other
// sandbox.
func ReviewPROf(sb *unstructured.Unstructured, repo string) (int, bool) {
	if r := sb.GetAnnotations()["repo"]; r != "" && r != repo {
		return 0, false
	}
	rest, ok := strings.CutPrefix(sb.GetName(), "review-"+repo+"-")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(rest)
	return n, err == nil && n > 0
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

// Result records the outcome of a finished invocation.
type Result struct {
	Err        error
	Output     string
	FinishedAt time.Time
}

// Launcher is the controller-facing interface (faked in tests).
type Launcher interface {
	// StartRecipe launches `factory recipe <name>` for key unless one is
	// already running, and applies RecipeOptions.Apply to its task output
	// when it ends: a controller pass harvests the finished invocation's
	// task output (HarvestedTaskOutput) via LastResult. Returns false if an
	// invocation for key is already in flight.
	StartRecipe(key string, opts RecipeOptions) bool
	// StartPRWatch launches `factory pr watch` for key unless one is
	// already running.
	StartPRWatch(key string, opts PRWatchOptions) bool
	// StartRevise launches `factory recipe revise` for key unless one is
	// already running; harvested as a recipe's run is, ReviseOptions.Apply
	// applied.
	StartRevise(key string, opts ReviseOptions) bool
	// StartRun launches `factory run <mode>` (plan, deploy or teardown
	// of one run, in that run's sandbox) unless one is already running.
	StartRun(key string, opts RunOptions) bool
	// StartApply launches `factory apply --action`: one write of a draft's
	// task output to its issue, with the member's token.
	StartApply(key string, opts ApplyOptions) bool
	// Recipes is the catalog of recipes factory runs, in the board's
	// order.
	Recipes(ctx context.Context) ([]boardv1alpha1.BoardRecipe, error)
	IsRunning(key string) bool
	// Running is the keys in flight that start with prefix.
	Running(prefix string) []string
	// Stop cancels key's invocation, if one is in flight; its result is
	// the cancellation.
	Stop(key string)
	// LastResult returns the outcome of the most recently finished
	// invocation for key, if any.
	LastResult(key string) (Result, bool)
}

// Runner is the real Launcher: it execs the factory binary bundled in the
// controller image, one single-flight child process per key.
type Runner struct {
	Prober TaskProber

	Binary string

	mu sync.Mutex
	// running holds each in-flight invocation's cancel.
	running map[string]context.CancelFunc
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
		running: make(map[string]context.CancelFunc),
		results: make(map[string]Result),
	}
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
		"--abort-on-cancel=false",
	}
	if opts.Engine != "" {
		args = append(args, "--engine", opts.Engine)
	}
	args = append(args, "--disclose="+strconv.FormatBool(opts.Disclose))
	return r.start(key, args, opts.GithubToken, timeout)
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
	// Apply is the action applied to the revise's task output when it
	// ends: a review's Update review posts its Review as the pending
	// review (post-review), replacing the one factory posted before; a
	// fix's follow-ups post the replies their Change carries
	// (post-replies). Empty applies nothing.
	Apply string
	// Inputs are the revise's inputs (factory --input name=value), such
	// as an Iterate's instruction.
	Inputs map[string]string
}

// StartRevise runs `factory recipe revise` and reads its result back as
// StartRecipe does, applying Apply to it. The revise runs on the
// sandbox's disk and engine as its task left them, so it takes no image,
// disk or engine.
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
	for _, name := range slices.Sorted(maps.Keys(opts.Inputs)) {
		args = append(args, "--input", name+"="+opts.Inputs[name])
	}
	return r.startWithPreflight(key, args, opts.GithubToken, timeout, r.harvest(opts.SandboxName, opts.Namespace, opts.RunName, opts.Apply, opts.GithubToken))
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
	// Detached from any reconcile context: the invocation outlives the
	// reconcile that started it.
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	r.running[key] = cancel
	r.mu.Unlock()

	go r.run(ctx, cancel, key, args, githubToken, pre)
	return true
}

func (r *Runner) Running(prefix string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var keys []string
	for key := range r.running {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

func (r *Runner) Stop(key string) {
	r.mu.Lock()
	cancel := r.running[key]
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
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

func (r *Runner) run(ctx context.Context, cancel context.CancelFunc, key string, args []string, githubToken string, pre *preflight) {
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

// TaskOutputFile is where a recipe task leaves its typed result
// (factory/pkg/taskoutput).
const TaskOutputFile = "task-output.yaml"

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
