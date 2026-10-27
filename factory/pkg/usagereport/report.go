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

package usagereport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
	factorysandbox "github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/sandbox"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/spool"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/taskapi"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/tokenusage"
)

const requestTimeout = 5 * time.Second

// Meta carries the issue/PR/workflow context attached to harvested records.
type Meta struct {
	Repo         string // "owner/name"
	TaskType     string // inferred from the task dir name when empty
	Sandbox      string
	Issue        int
	PR           int
	Issues       []int
	Workflow     string // workflow session id, e.g. "issue-42"
	WorkflowName string
}

// Enabled reports whether usage reporting is configured.
func Enabled() bool {
	return collectorURL() != ""
}

func collectorURL() string {
	return strings.TrimRight(os.Getenv("COLLECTOR_URL"), "/")
}

// TaskFileReader reads a file of one task directory by name; a file
// that is not there is empty, or os.ErrNotExist.
type TaskFileReader func(name string) ([]byte, error)

// readFile is a task file's content, "" when it is not there.
func readFile(read TaskFileReader, name string) (string, error) {
	data, err := read(name)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func readTaskUsage(read TaskFileReader, taskDir string) (*Stats, error) {
	data, err := readFile(read, "token-usage.json")
	if err == nil && data == "" {
		data, err = readFile(read, "llm-usage.json")
	}
	if err != nil {
		return nil, fmt.Errorf("reading usage file from %s: %w", taskDir, err)
	}
	if data == "" {
		return nil, nil
	}
	var stats Stats
	if err := json.Unmarshal([]byte(data), &stats); err != nil {
		return nil, fmt.Errorf("parsing usage file from %s: %w", taskDir, err)
	}
	if len(stats.Models) == 0 {
		return nil, nil
	}
	return &stats, nil
}

// Publish upserts a usage record in the collector.
func Publish(ctx context.Context, rec UsageRecord) error {
	return postJSON(ctx, "/v1/usage", rec)
}

// PublishSubject upserts issue/PR metadata (state, timestamps) in the
// collector, which joins it onto rollups to expose age and status.
func PublishSubject(ctx context.Context, sub Subject) error {
	return postJSON(ctx, "/v1/subjects", sub)
}

// ReportIssueSubject publishes an issue's state and timestamps.
// Best-effort: no-op when disabled or issue is nil; failures are logged.
func ReportIssueSubject(ctx context.Context, repo string, issue *githubv39.Issue) {
	if !Enabled() || issue == nil || issue.GetNumber() == 0 {
		return
	}
	sub := Subject{
		Key:       tokenusage.SubjectKey("issue", issue.GetNumber()),
		Repo:      repo,
		Kind:      "issue",
		Number:    issue.GetNumber(),
		State:     issue.GetState(),
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if t := issue.GetCreatedAt(); !t.IsZero() {
		sub.CreatedAt = t.UTC().Format(time.RFC3339)
	}
	if t := issue.GetClosedAt(); !t.IsZero() {
		sub.ClosedAt = t.UTC().Format(time.RFC3339)
	}
	if err := PublishSubject(ctx, sub); err != nil {
		klog.Warningf("usagereport: publishing subject %s: %v", sub.Key, err)
	}
}

// ReportPRSubject publishes a PR's state and timestamps.
// Best-effort: no-op when disabled or pr is nil; failures are logged.
func ReportPRSubject(ctx context.Context, repo string, pr *githubv39.PullRequest) {
	if !Enabled() || pr == nil || pr.GetNumber() == 0 {
		return
	}
	sub := Subject{
		Key:       tokenusage.SubjectKey("pr", pr.GetNumber()),
		Repo:      repo,
		Kind:      "pr",
		Number:    pr.GetNumber(),
		State:     pr.GetState(),
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if t := pr.GetCreatedAt(); !t.IsZero() {
		sub.CreatedAt = t.UTC().Format(time.RFC3339)
	}
	if t := pr.GetClosedAt(); !t.IsZero() {
		sub.ClosedAt = t.UTC().Format(time.RFC3339)
	}
	if err := PublishSubject(ctx, sub); err != nil {
		klog.Warningf("usagereport: publishing subject %s: %v", sub.Key, err)
	}
}

// ReportPRFeedback uploads human feedback captured on a closed pull request to
// the collector's PUT /v1/prs/{owner}/{repo}/{n}/feedback endpoint.
// Best-effort: no-op when disabled; returns an error on upload failure.
func ReportPRFeedback(ctx context.Context, owner, repo string, pr int, data []byte) error {
	if !Enabled() {
		return nil
	}
	u := fmt.Sprintf("%s/v1/prs/%s/%s/%d/feedback", collectorURL(), url.PathEscape(owner), url.PathEscape(repo), pr)
	ctx, cancel := context.WithTimeout(ctx, archiveTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-ndjson")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("reporting feedback for %s/%s#%d: %w", owner, repo, pr, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusNotImplemented:
		return nil
	default:
		return fmt.Errorf("reporting feedback for %s/%s#%d: collector returned status %d", owner, repo, pr, resp.StatusCode)
	}
}

// ReportPROutcome uploads an outcome record for a closed pull request to
// the collector's PUT /v1/outcomes/{owner}/{repo}/{pr} endpoint.
// Best-effort: no-op when disabled; returns an error on upload failure.
func ReportPROutcome(ctx context.Context, outcome *tokenusage.OutcomeRecord) error {
	if !Enabled() || outcome == nil {
		return nil
	}
	parts := strings.Split(outcome.Repo, "/")
	if len(parts) != 2 {
		return fmt.Errorf("invalid outcome repo %q", outcome.Repo)
	}
	owner, repo := parts[0], parts[1]
	u := fmt.Sprintf("%s/v1/outcomes/%s/%s/%d", collectorURL(), url.PathEscape(owner), url.PathEscape(repo), outcome.PR)
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	data, err := json.Marshal(outcome)
	if err != nil {
		return fmt.Errorf("marshaling outcome for %s#%d: %w", outcome.Repo, outcome.PR, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("reporting outcome for %s#%d: %w", outcome.Repo, outcome.PR, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusNotImplemented:
		return nil
	default:
		return fmt.Errorf("reporting outcome for %s#%d: collector returned status %d", outcome.Repo, outcome.PR, resp.StatusCode)
	}
}

// ListTasksForPR queries the collector's task index for tasks belonging to a PR.
func ListTasksForPR(ctx context.Context, owner, repo string, pr int) ([]tokenusage.TaskRecord, error) {
	if !Enabled() {
		return nil, nil
	}
	u := fmt.Sprintf("%s/v1/tasks?repo=%s&pr=%d", collectorURL(), url.QueryEscape(owner+"/"+repo), pr)
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("listing tasks returned %d", resp.StatusCode)
	}
	var out struct {
		Tasks []tokenusage.TaskRecord `json:"tasks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Tasks, nil
}

func postJSON(ctx context.Context, path string, v any) error {
	base := collectorURL()
	if base == "" {
		return nil
	}
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("collector returned status %d", resp.StatusCode)
	}
	return nil
}

func readTaskToolTelemetry(read TaskFileReader) (*ToolTelemetry, error) {
	data, err := readFile(read, "tool-telemetry.json")
	if err != nil || data == "" {
		return nil, nil
	}
	var telemetry ToolTelemetry
	if err := json.Unmarshal([]byte(data), &telemetry); err != nil {
		return nil, nil
	}
	return &telemetry, nil
}

// TaskFiles is how a harvest reaches a task's files: the sandbox's task
// server (taskapi.ServerSandbox).
type TaskFiles interface {
	// AllFiles lists the task's files, those one directory down as
	// "<dir>/<name>"; never its env or secrets.
	AllFiles(ctx context.Context, id string) ([]string, error)
	ReadFile(ctx context.Context, id, name string) ([]byte, error)
}

// HarvestTask publishes a task's usage and archives its files.
// Best-effort: failures are logged.
func HarvestTask(ctx context.Context, sb TaskFiles, id string, meta Meta) {
	_ = harvestTask(ctx, sb, id, meta)
}

// harvestTask publishes a task's usage and archives its files, and
// returns the first error that kept either from the collector (logged
// too).
func harvestTask(ctx context.Context, sb TaskFiles, id string, meta Meta) error {
	if !Enabled() {
		return nil
	}
	taskDir := spool.TaskDir(id)
	err := harvestUsage(ctx, taskDir, func(name string) ([]byte, error) { return sb.ReadFile(ctx, id, name) }, meta)
	if err != nil {
		klog.Warningf("usagereport: %v", err)
	}
	if ferr := archiveTask(ctx, sb, id, meta); ferr != nil {
		klog.Warningf("usagereport: %v", ferr)
		if err == nil {
			err = ferr
		}
	}
	return err
}

func harvestUsage(ctx context.Context, taskDir string, read TaskFileReader, meta Meta) error {
	stats, err := readTaskUsage(read, taskDir)
	if err != nil {
		return err
	}
	if stats == nil {
		return nil
	}
	taskType := meta.TaskType
	if taskType == "" {
		taskType = taskTypeFromDir(taskDir)
	}
	rec := UsageRecord{
		Key:          meta.Sandbox + ":" + taskDir,
		Repo:         meta.Repo,
		TaskType:     taskType,
		TaskDir:      taskDir,
		Sandbox:      meta.Sandbox,
		Issue:        meta.Issue,
		PR:           meta.PR,
		Issues:       meta.Issues,
		Workflow:     meta.Workflow,
		WorkflowName: meta.WorkflowName,
		RecordedAt:   time.Now().UTC().Format(time.RFC3339),
		Stats:        *stats,
	}

	if tt, err := readTaskToolTelemetry(read); err == nil && tt != nil {
		for i := range tt.ShellCalls {
			if tt.ShellCalls[i].Repo == "" {
				tt.ShellCalls[i].Repo = meta.Repo
			}
			if tt.ShellCalls[i].TaskType == "" {
				tt.ShellCalls[i].TaskType = taskType
			}
			if tt.ShellCalls[i].Sandbox == "" {
				tt.ShellCalls[i].Sandbox = meta.Sandbox
			}
			if tt.ShellCalls[i].TaskDir == "" {
				tt.ShellCalls[i].TaskDir = taskDir
			}
		}
		rec.ToolTelemetry = tt
	}

	if err := Publish(ctx, rec); err != nil {
		return fmt.Errorf("publishing usage for %s: %w", rec.Key, err)
	}
	klog.V(2).Infof("usagereport: published usage for %s", rec.Key)
	return nil
}

// unarchived are the task files the archive leaves out: generated from
// recipe.source.yaml (recipe.yaml, the step script), or bookkeeping of a
// process long gone (pid, lease).
var unarchived = map[string]bool{"recipe.yaml": true, "steps/uses.sh": true, "pid": true, "lease": true}

// TranscriptFile is a task's session transcript (acpd's stream.ndjson),
// by the name the task server serves it.
const TranscriptFile = taskapi.SessionDir + "/stream.ndjson"

// archiveTimeout bounds one task's upload: transcripts run to megabytes.
const archiveTimeout = 2 * time.Minute

// archiveTask sends the task's files to the collector, which archives
// them: the transcript (session/stream.ndjson), what the task was given
// and ran (inputs.json, recipe.source.yaml), what it produced and pushed,
// and its logs. A task server that predates listing subdirectories sends
// the top level only; a collector without an archive keeps nothing.
func archiveTask(ctx context.Context, sb TaskFiles, id string, meta Meta) error {
	if meta.Sandbox == "" {
		return nil
	}
	names, err := sb.AllFiles(ctx, id)
	if err != nil {
		return fmt.Errorf("listing the files of task %s: %w", id, err)
	}
	// A task server that lists the top level only (#2035's) still serves
	// the transcript by name.
	if !slices.Contains(names, TranscriptFile) {
		names = append(names, TranscriptFile)
	}
	files := map[string][]byte{}
	for _, name := range names {
		if unarchived[name] {
			continue
		}
		data, err := sb.ReadFile(ctx, id, name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("reading %s of task %s: %w", name, id, err)
		}
		if len(data) > 0 {
			files[name] = data
		}
	}
	if len(files) == 0 {
		return nil
	}
	body, err := tokenusage.TarFiles(files)
	if err != nil {
		return err
	}

	q := url.Values{"repo": {meta.Repo}}
	if meta.Issue > 0 {
		q.Set("issue", strconv.Itoa(meta.Issue))
	}
	if meta.PR > 0 {
		q.Set("pr", strconv.Itoa(meta.PR))
	}
	taskType := meta.TaskType
	if taskType == "" {
		taskType = taskTypeFromDir(id)
	}
	q.Set("taskType", taskType)
	u := collectorURL() + "/v1/tasks/" + url.PathEscape(meta.Sandbox) + "/" + url.PathEscape(id) + "?" + q.Encode()

	ctx, cancel := context.WithTimeout(ctx, archiveTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-tar")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("archiving task %s: %w", id, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusNotImplemented:
		return nil
	default:
		return fmt.Errorf("archiving task %s: collector returned status %d", id, resp.StatusCode)
	}
}

// HarvestSandbox harvests every task dir in a sandbox, usage and files,
// through its task server: before a sandbox is suspended, while its pod
// is up, and before it is deleted. A suspended sandbox harvested since its
// last activity is not woken for it: nothing ran in it since. A harvest
// that published everything marks the sandbox harvested
// (AnnotationHarvestedAt). Best-effort; it reports whether the sandbox's
// usage and traces are all in the collector.
func HarvestSandbox(ctx context.Context, namespace, sandboxName string, meta Meta) bool {
	if !Enabled() {
		return false
	}
	if sb, err := getSandbox(ctx, namespace, sandboxName); err == nil && harvestedWhileSuspended(sb) {
		klog.V(2).Infof("usagereport: sandbox %s is suspended and harvested since its last activity; not waking it", sandboxName)
		return true
	}
	meta.Sandbox = sandboxName
	sb, err := connectTasks(ctx, namespace, sandboxName)
	if err != nil {
		klog.Warningf("usagereport: connecting to sandbox %s's task server for usage sweep: %v", sandboxName, err)
		return false
	}
	defer sb.Close()
	if !harvestTasks(ctx, sb, meta) {
		return false
	}
	if err := markHarvested(ctx, namespace, sandboxName, time.Now().UTC()); err != nil {
		klog.Warningf("usagereport: marking sandbox %s harvested: %v", sandboxName, err)
	}
	return true
}

// harvestedWhileSuspended reports whether sb is scaled to zero and was
// harvested no earlier than its last activity.
func harvestedWhileSuspended(sb *unstructured.Unstructured) bool {
	replicas, found, err := unstructured.NestedInt64(sb.Object, "spec", "replicas")
	if err != nil || !found || replicas != 0 {
		return false
	}
	at, err := time.Parse(time.RFC3339, sb.GetAnnotations()[factorysandbox.AnnotationHarvestedAt])
	if err != nil {
		return false
	}
	return !factorysandbox.LastActivity(sb).After(at)
}

// getSandbox reads a sandbox; a var for the tests.
var getSandbox = func(ctx context.Context, namespace, name string) (*unstructured.Unstructured, error) {
	kc, err := clients.NewKubernetesClient()
	if err != nil {
		return nil, err
	}
	return kc.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
}

// markHarvested sets AnnotationHarvestedAt on a sandbox; a var for the
// tests.
var markHarvested = func(ctx context.Context, namespace, name string, at time.Time) error {
	kc, err := clients.NewKubernetesClient()
	if err != nil {
		return err
	}
	patch := fmt.Sprintf(`{"metadata":{"annotations":{%q:%q}}}`, factorysandbox.AnnotationHarvestedAt, at.Format(time.RFC3339))
	_, err = kc.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Patch(ctx, name, types.MergePatchType, []byte(patch), metav1.PatchOptions{})
	return err
}

// taskLister is what HarvestSandbox reads a sandbox's tasks through: its
// task server (taskapi.ServerSandbox), which lists every task directory,
// a classic command's or a recipe's, and serves their files.
type taskLister interface {
	TaskFiles
	List(ctx context.Context) ([]spool.Entry, error)
	Close()
}

// connectTasks reaches a sandbox's task server; a var for the tests.
var connectTasks = func(ctx context.Context, namespace, name string) (taskLister, error) {
	return taskapi.ConnectServer(ctx, namespace, name)
}

// harvestTasks harvests each of the sandbox's task dirs, its task type
// inferred from its name, and reports whether all of them were.
func harvestTasks(ctx context.Context, sb taskLister, meta Meta) bool {
	entries, err := sb.List(ctx)
	if err != nil {
		klog.Warningf("usagereport: listing the tasks in sandbox %s: %v", meta.Sandbox, err)
		return false
	}
	ok := true
	for _, e := range entries {
		m := meta
		m.TaskType = "" // infer per dir
		if err := harvestTask(ctx, sb, e.ID, m); err != nil {
			ok = false
		}
	}
	return ok
}

// taskTypeFromDir infers the task type from a task dir name such as
// "fix-20260713-101500", "agent-mychore-20260713-101500".
func taskTypeFromDir(taskDir string) string {
	base := path.Base(taskDir)
	if idx := strings.Index(base, "-"); idx > 0 {
		return base[:idx]
	}
	return base
}

// PostWorkflowSummaryIfNeeded posts a one-time token-usage summary comment
// on a workflow issue. The collector's mark-summarized CAS guarantees the
// comment is posted at most once even across watch-loop restarts.
// Best-effort: failures are logged, never returned.
func PostWorkflowSummaryIfNeeded(ctx context.Context, ghClient *githubv39.Client, owner, repo string, issueNum int) {
	if !Enabled() {
		return
	}
	session := fmt.Sprintf("issue-%d", issueNum)
	rollup, err := fetchWorkflowRollup(ctx, owner+"/"+repo, session)
	if err != nil {
		klog.Warningf("usagereport: fetching workflow rollup for %s: %v", session, err)
		return
	}
	if rollup == nil {
		return // no usage recorded, nothing to summarize
	}

	alreadyPosted, err := markSummarized(ctx, session)
	if err != nil {
		klog.Warningf("usagereport: marking workflow %s summarized: %v", session, err)
		return
	}
	if alreadyPosted {
		return
	}

	body := FormatWorkflowSummary(rollup)
	if _, _, err := ghClient.Issues.CreateComment(ctx, owner, repo, issueNum, &githubv39.IssueComment{
		Body: githubv39.String(body),
	}); err != nil {
		klog.Errorf("usagereport: posting workflow usage summary on #%d: %v", issueNum, err)
		return
	}
	klog.Infof("usagereport: posted workflow usage summary on issue #%d", issueNum)
}

func fetchWorkflowRollup(ctx context.Context, repo, session string) (*Rollup, error) {
	base := collectorURL()
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	u := fmt.Sprintf("%s/v1/usage/rollups/workflows/%s?repo=%s", base, url.PathEscape(session), url.QueryEscape(repo))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("collector returned status %d", resp.StatusCode)
	}
	var rollup Rollup
	if err := json.NewDecoder(resp.Body).Decode(&rollup); err != nil {
		return nil, err
	}
	return &rollup, nil
}

func markSummarized(ctx context.Context, session string) (bool, error) {
	base := collectorURL()
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	u := fmt.Sprintf("%s/v1/workflows/%s/mark-summarized", base, url.PathEscape(session))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
	if err != nil {
		return false, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("collector returned status %d", resp.StatusCode)
	}
	var result struct {
		AlreadyPosted bool `json:"alreadyPosted"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, err
	}
	return result.AlreadyPosted, nil
}

// FormatWorkflowSummary renders the GitHub comment body for a workflow
// usage rollup.
func FormatWorkflowSummary(r *Rollup) string {
	var b strings.Builder
	b.WriteString("## Gemini token usage summary\n\n")
	if r.WorkflowName != "" {
		fmt.Fprintf(&b, "Workflow: **%s**\n", r.WorkflowName)
	}
	fmt.Fprintf(&b, "Tasks: %d", r.TaskCount)
	if len(r.PRs) > 0 {
		prs := make([]string, 0, len(r.PRs))
		for _, n := range r.PRs {
			prs = append(prs, fmt.Sprintf("#%d", n))
		}
		fmt.Fprintf(&b, " | Pull requests: %s", strings.Join(prs, ", "))
	}
	b.WriteString("\n\n")
	b.WriteString("| Model | Requests | Errors | Input | Output | Cached | Thoughts | Total |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|\n")

	models := make([]string, 0, len(r.Stats.Models))
	for m := range r.Stats.Models {
		models = append(models, m)
	}
	sort.Strings(models)

	var total ModelUsage
	for _, m := range models {
		mu := r.Stats.Models[m]
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %d | %d | %d | %d |\n",
			m, mu.API.TotalRequests, mu.API.TotalErrors,
			mu.Tokens.Input, mu.Tokens.Output, mu.Tokens.Cached, mu.Tokens.Thoughts, mu.Tokens.Total)
		total.API.TotalRequests += mu.API.TotalRequests
		total.API.TotalErrors += mu.API.TotalErrors
		total.Tokens.Input += mu.Tokens.Input
		total.Tokens.Output += mu.Tokens.Output
		total.Tokens.Cached += mu.Tokens.Cached
		total.Tokens.Thoughts += mu.Tokens.Thoughts
		total.Tokens.Total += mu.Tokens.Total
	}
	if len(models) > 1 {
		fmt.Fprintf(&b, "| **Total** | %d | %d | %d | %d | %d | %d | %d |\n",
			total.API.TotalRequests, total.API.TotalErrors,
			total.Tokens.Input, total.Tokens.Output, total.Tokens.Cached, total.Tokens.Thoughts, total.Tokens.Total)
	}
	return b.String()
}
