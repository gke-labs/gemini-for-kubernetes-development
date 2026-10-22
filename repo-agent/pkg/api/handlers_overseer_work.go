package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/models"
)

// The Overseer page's work feed (docs/design/overseer-work-view.md): one
// row per issue (with the PR its fix opened), per PR, or per agent
// without either, built from the overseer's sandboxes, the runs they
// recorded and the watch's queue. No GitHub.

// OverseerWork is GET /api/overseers/:name/work.
type OverseerWork struct {
	Items []OverseerWorkItem `json:"items"`
	// QueueError says why the queue is missing from the rows, when it is.
	QueueError string `json:"queueError,omitempty"`
	// Rules is the watch rules' summary (QueueRules), when the watch
	// serves one.
	Rules *QueueRules `json:"rules,omitempty"`
}

// OverseerWorkItem is one row.
type OverseerWorkItem struct {
	// Key is the row's: issue-<n>, pr-<n>, or sandbox-<name> for an agent.
	Key string `json:"key"`
	// Kind is issue, pr or agent.
	Kind string `json:"kind"`
	Repo string `json:"repo,omitempty"`
	// Number is the issue's (an issue row) or the PR's (a PR row).
	Number int `json:"number,omitempty"`
	// PR is the PR an issue's fix opened.
	PR      int    `json:"pr,omitempty"`
	HTMLURL string `json:"htmlUrl,omitempty"`
	PRURL   string `json:"prUrl,omitempty"`
	Title   string `json:"title,omitempty"`
	// Workflow marks an issue a workflow (wf-issue-<n>) works on.
	Workflow bool `json:"workflow,omitempty"`
	// Section is where the row goes: attention, working, queued, idle or
	// agents. Reason says why a row needs attention.
	Section   string                `json:"section"`
	Reason    string                `json:"reason,omitempty"`
	Sandboxes []OverseerWorkSandbox `json:"sandboxes"`
	// Sessions are the runs the row's sandboxes recorded, newest first:
	// its chips.
	Sessions []models.RunSession `json:"sessions"`
	// Queued is the row's work in the watch's queue.
	Queued []OverseerQueuedTask `json:"queued,omitempty"`
	// Cooldown is the row's task the watch holds back until its cooldown
	// ends, when it does: the row's "cooldown until" label.
	Cooldown *OverseerCooldown `json:"cooldown,omitempty"`
	// Failure is the row's latest task the watch ran that failed and has
	// not succeeded since, with why: a run can succeed in its sandbox and
	// its task still fail applying the result (posting replies, opening
	// the PR), which only the watch knows.
	Failure *OverseerFailure `json:"failure,omitempty"`
	// UpdatedAt is the latest a run of the row started or ended.
	UpdatedAt string `json:"updatedAt,omitempty"`
}

// OverseerWorkSandbox is one of a row's sandboxes.
type OverseerWorkSandbox struct {
	Name     string `json:"name"`
	Type     string `json:"type,omitempty"`
	User     string `json:"user,omitempty"`
	Replicas int64  `json:"replicas"`
	// Pod is running, starting, paused, evicted or failed.
	Pod       string `json:"pod"`
	TaskType  string `json:"taskType,omitempty"`
	TaskState string `json:"taskState,omitempty"`
	Engine    string `json:"engine,omitempty"`
	// RequiresHuman is why the sandbox's last run failed in a way a rerun
	// would repeat (annoRequiresHuman): the watch dispatches nothing to it
	// until someone clears it.
	RequiresHuman string `json:"requiresHuman,omitempty"`
}

// annoRequiresHuman is factory's mark on a sandbox that needs a person
// before anything runs in it again (factory/pkg/sandbox).
const annoRequiresHuman = "factory.gemini.google.com/requires-human"

// OverseerQueuedTask is a row's task in the watch's queue.
type OverseerQueuedTask struct {
	FileName string `json:"fileName"`
	Type     string `json:"type"`
	// Recipe is what the task runs (fix, care-ci, …).
	Recipe string `json:"recipe,omitempty"`
	// State is incoming or processing.
	State      string `json:"state"`
	Rank       int    `json:"rank,omitempty"`
	Priority   string `json:"priority,omitempty"`
	Assignee   string `json:"assignee,omitempty"`
	EnqueuedAt string `json:"enqueuedAt,omitempty"`
}

// OverseerCooldown is a row's task held back by its cooldown: a workflow's
// declared one after a run, or 20 minutes after a failed run.
type OverseerCooldown struct {
	Until           string `json:"until"`
	Workflow        string `json:"workflow,omitempty"`
	LastStatus      string `json:"lastStatus,omitempty"`
	LastCompletedAt string `json:"lastCompletedAt,omitempty"`
}

// OverseerFailure is a row's failed task, as the watch recorded it.
type OverseerFailure struct {
	Type   string `json:"type"`
	Recipe string `json:"recipe,omitempty"`
	Error  string `json:"error"`
	At     string `json:"at"`
}

// queueRecipes is the recipe each queue task type runs.
var queueRecipes = map[string]string{
	"issue-fix":      "fix",
	"pr-investigate": "care-ci",
	"pr-comments":    "care-comments",
	"pr-iterate":     "care-rebase",
	"pr-review":      "review",
	"agent-chore":    "agent",
}

// fetchOverseerQueue is the watch's queue in the overseer's controller
// (:13338/api/v1/queue). A var for the tests.
var fetchOverseerQueue = func(ctx context.Context, s *Server, name string) (*QueueResponse, error) {
	namespace := "overseer-" + name
	pods, err := s.K8sManager.Clientset.CoreV1().Pods(namespace).List(ctx, v1.ListOptions{LabelSelector: "sandbox=" + namespace})
	if err != nil {
		return nil, fmt.Errorf("finding the overseer's controller: %w", err)
	}
	if len(pods.Items) == 0 || pods.Items[0].Status.PodIP == "" {
		return nil, fmt.Errorf("the overseer's controller is not running")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://%s:13338/api/v1/queue", pods.Items[0].Status.PodIP), nil)
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("the watch's queue is unreachable (it may be mid-cycle): %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the watch's queue answered %d", resp.StatusCode)
	}
	var q QueueResponse
	if err := json.NewDecoder(resp.Body).Decode(&q); err != nil {
		return nil, fmt.Errorf("reading the watch's queue: %w", err)
	}
	return &q, nil
}

func (s *Server) getOverseerWork(c *gin.Context) {
	name := c.Param("name")
	if !safeTaskName.MatchString(name) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid overseer name"})
		return
	}
	ctx := c.Request.Context()
	namespace := "overseer-" + name
	sandboxes, err := s.K8sManager.ListSandboxes(ctx, namespace, "")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list the overseer's sandboxes", "details": err.Error()})
		return
	}
	pods, err := s.K8sManager.Clientset.CoreV1().Pods(namespace).List(ctx, v1.ListOptions{})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list the overseer's pods", "details": err.Error()})
		return
	}
	work := OverseerWork{}
	q, err := fetchOverseerQueue(ctx, s, name)
	if err != nil {
		work.QueueError = err.Error()
	} else if q != nil {
		work.Rules = q.Rules
	}
	work.Items = buildOverseerWork(namespace, sandboxes.Items, pods.Items, q)
	c.JSON(http.StatusOK, work)
}

// trailingNumber is the number a sandbox name ends in.
var trailingNumber = regexp.MustCompile(`-(\d+)$`)

// itemNumber is the issue or PR number in a GitHub URL, and which.
func itemNumber(htmlURL string) (kind string, n int) {
	parts := strings.Split(strings.Trim(htmlURL, "/"), "/")
	if len(parts) < 2 {
		return "", 0
	}
	n, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		return "", 0
	}
	switch parts[len(parts)-2] {
	case "issues":
		return "issue", n
	case "pull":
		return "pr", n
	}
	return "", 0
}

// sandboxWork is what a sandbox works on: an issue (and the PR its fix
// opened), a PR, or neither, and whether a workflow does.
type sandboxWork struct {
	issue, pr int
	workflow  bool
}

func workOf(sb *unstructured.Unstructured) sandboxWork {
	labels, a := sb.GetLabels(), sb.GetAnnotations()
	name := sb.GetName()
	var w sandboxWork
	w.issue, _ = strconv.Atoi(labels[factorycli.LabelIssue])
	w.pr, _ = strconv.Atoi(labels["factory.gemini.google.com/pr"])
	number := 0
	if m := trailingNumber.FindStringSubmatch(name); m != nil {
		number, _ = strconv.Atoi(m[1])
	}
	switch {
	case strings.HasPrefix(name, "wf-issue-"):
		w.issue, w.workflow = number, true
	case strings.HasPrefix(name, "review-") && w.pr == 0:
		// A review sandbox carries no PR label: its name says which.
		w.pr = number
	case strings.HasPrefix(name, "fix-") && w.issue == 0 && number != 0 && number != w.pr:
		// An issue's fix sandbox from before the issue label: its name is
		// the issue's (its htmlURL turned to the PR once open-pr aliased
		// it). A PR's own is fix-<repo>-<pr>.
		w.issue = number
	}
	if w.issue == 0 && w.pr == 0 {
		switch kind, n := itemNumber(a["htmlURL"]); kind {
		case "issue":
			w.issue = n
		case "pr":
			w.pr = n
		}
	}
	return w
}

// podState is a sandbox's pod as the row shows it (OverseerWorkSandbox.Pod).
func podState(sb *unstructured.Unstructured, pods map[string]*corev1.Pod) string {
	if replicas, found, _ := unstructured.NestedInt64(sb.Object, "spec", "replicas"); found && replicas == 0 {
		return "paused"
	}
	pod := pods[sb.GetName()]
	switch {
	case pod == nil:
		return "starting"
	case pod.Status.Reason == "Evicted":
		return "evicted"
	case pod.Status.Phase == corev1.PodFailed:
		return "failed"
	case pod.Status.Phase == corev1.PodRunning:
		return "running"
	}
	return "starting"
}

// classicRecipes is the recipe each classic task type (last-task-type)
// became.
var classicRecipes = map[string]string{
	"fix-issue":        "fix",
	"investigate":      "care-ci",
	"address-comments": "care-comments",
	"iterate":          "care-rebase",
}

// runSessions are the runs sb recorded, as chips. A sandbox that recorded
// none, whose tasks were the classic commands', gets one from its last
// task, with no session to open.
func runSessions(sb *unstructured.Unstructured) []models.RunSession {
	var sessions []models.RunSession
	annotations := sb.GetAnnotations()
	if len(factorycli.Runs(annotations)) == 0 {
		taskType := annotations[annoLastTaskType]
		if taskType == "" {
			return nil
		}
		recipe := firstNonEmpty(classicRecipes[taskType], taskType)
		session := models.RunSession{Recipe: recipe, Sandbox: sb.GetName(), Engine: sandboxEngine(annotations, "gemini"), State: annotations[annoTaskState]}
		session.Status = runStatus(session)
		if session.State == "" || session.State == "Completed" {
			session.Status = "done"
		}
		if t := firstNonEmpty(annotations["sandbox.gemini.google.com/completion-time"], annotations["sandbox.gemini.google.com/last-task-time"]); t != "" {
			session.EndedAt = t
		}
		return []models.RunSession{session}
	}
	for _, run := range factorycli.Runs(annotations) {
		recipe := run.Recipe
		if recipe == "" {
			recipe = factorycli.RunTaskType(run.Key)
		}
		session := models.RunSession{
			Recipe:    recipe,
			Sandbox:   sb.GetName(),
			Engine:    sandboxEngine(annotations, "gemini"),
			Task:      run.SessionOf(),
			Run:       run.Key,
			Kind:      run.Kind,
			Source:    run.Source,
			State:     run.State,
			StartedAt: run.StartedAt.UTC().Format(time.RFC3339),
			Output:    annotations[factorycli.OutputAnnotation(run.Key)] != "",
			Applied:   factorycli.Applied(annotations, factorycli.AppliedAnnotation(run.Key)),
		}
		if run.EndedAt != nil {
			session.EndedAt = run.EndedAt.UTC().Format(time.RFC3339)
		}
		session.Status = runStatus(session)
		sessions = append(sessions, session)
	}
	return sessions
}

// buildOverseerWork makes the rows from the overseer's sandboxes, its
// pods and its queue (nil when unreachable).
func buildOverseerWork(namespace string, sandboxes []unstructured.Unstructured, podList []corev1.Pod, q *QueueResponse) []OverseerWorkItem {
	pods := map[string]*corev1.Pod{}
	for i := range podList {
		if sb := podList[i].Labels["sandbox"]; sb != "" {
			pods[sb] = &podList[i]
		}
	}
	// An issue's fix aliased to its PR: the PR's work is the issue's row.
	prIssue := map[int]int{}
	var mine []*unstructured.Unstructured
	for i := range sandboxes {
		sb := &sandboxes[i]
		labels := sb.GetLabels()
		sType := labels["sandbox.gemini.google.com/type"]
		if sType == "" {
			sType = labels["sandbox-type"]
		}
		if sType == "overseer" || sb.GetName() == namespace {
			continue // the overseer's controller
		}
		mine = append(mine, sb)
		if w := workOf(sb); w.issue != 0 && w.pr != 0 {
			prIssue[w.pr] = w.issue
		}
	}
	rows := map[string]*OverseerWorkItem{}
	row := func(issue, pr int, sbName string) *OverseerWorkItem {
		if issue == 0 && pr != 0 {
			issue = prIssue[pr]
		}
		key, kind, number := "", "", 0
		switch {
		case issue != 0:
			key, kind, number = "issue-"+strconv.Itoa(issue), "issue", issue
		case pr != 0:
			key, kind, number = "pr-"+strconv.Itoa(pr), "pr", pr
		default:
			key, kind = "sandbox-"+sbName, "agent"
		}
		r := rows[key]
		if r == nil {
			r = &OverseerWorkItem{Key: key, Kind: kind, Number: number, Sandboxes: []OverseerWorkSandbox{}, Sessions: []models.RunSession{}}
			rows[key] = r
		}
		if kind == "issue" && r.PR == 0 {
			for p, i := range prIssue {
				if i == issue {
					r.PR = p
				}
			}
		}
		return r
	}
	for _, sb := range mine {
		w := workOf(sb)
		r := row(w.issue, w.pr, sb.GetName())
		labels, a := sb.GetLabels(), sb.GetAnnotations()
		r.Workflow = r.Workflow || w.workflow
		if r.Repo == "" {
			r.Repo = a["repo"]
		}
		if r.Title == "" {
			r.Title = firstNonEmpty(a["title"], a["description"])
		}
		switch kind, _ := itemNumber(a["htmlURL"]); {
		case kind == "issue" && r.HTMLURL == "":
			r.HTMLURL = a["htmlURL"]
		case kind == "pr" && r.Kind == "issue" && r.PRURL == "":
			r.PRURL = a["htmlURL"]
		case kind == "pr" && r.HTMLURL == "":
			r.HTMLURL = a["htmlURL"]
		}
		replicas, _, _ := unstructured.NestedInt64(sb.Object, "spec", "replicas")
		sType := labels["sandbox.gemini.google.com/type"]
		if sType == "" {
			sType = labels["sandbox-type"]
		}
		r.Sandboxes = append(r.Sandboxes, OverseerWorkSandbox{
			Name: sb.GetName(), Type: sType, User: labels[labelFactoryUser], Replicas: replicas,
			Pod: podState(sb, pods), TaskType: a[annoLastTaskType], TaskState: a[annoTaskState],
			Engine: sandboxEngine(a, "gemini"), RequiresHuman: a[annoRequiresHuman],
		})
		for _, session := range runSessions(sb) {
			if w.workflow && session.Recipe == "agent" {
				session.Recipe = "workflow" // a chore's run, a workflow's step
			}
			r.Sessions = append(r.Sessions, session)
		}
	}
	if q != nil {
		for state, tasks := range map[string][]QueueTaskItem{"incoming": q.Incoming, "processing": q.Processing} {
			for _, t := range tasks {
				issue, pr := 0, 0
				switch kind, n := itemNumber(t.URL); kind {
				case "issue":
					issue = n
				case "pr":
					pr = n
				}
				if issue == 0 && pr == 0 {
					continue // a chore with no issue: its agent row is its sandbox's
				}
				r := row(issue, pr, "")
				r.Queued = append(r.Queued, OverseerQueuedTask{
					FileName: t.FileName, Type: t.Type, Recipe: queueRecipes[t.Type], State: state,
					Rank: t.Rank, Priority: t.Priority, Assignee: t.Assignee, EnqueuedAt: t.EnqueuedAt,
				})
			}
		}
	}
	if q != nil {
		for _, c := range q.Cooldowns {
			issue := c.Number
			if kind, n := itemNumber(c.URL); kind == "issue" {
				issue = n
			}
			if issue == 0 {
				continue
			}
			r := row(issue, 0, "")
			if r.HTMLURL == "" {
				r.HTMLURL = c.URL
			}
			if r.Cooldown == nil || c.Until > r.Cooldown.Until {
				r.Cooldown = &OverseerCooldown{Until: c.Until, Workflow: c.Workflow, LastStatus: c.LastStatus, LastCompletedAt: c.LastCompletedAt}
			}
		}
	}
	if q != nil {
		for _, f := range q.Failures {
			issue, pr := 0, 0
			switch kind, n := itemNumber(f.URL); kind {
			case "issue":
				issue = n
			case "pr":
				pr = n
			}
			if issue == 0 && pr == 0 {
				continue // a chore's failure: its agent row shows its sandbox's runs
			}
			r := row(issue, pr, "")
			if r.HTMLURL == "" {
				r.HTMLURL = f.URL
			}
			if r.Failure == nil || f.At > r.Failure.At {
				r.Failure = &OverseerFailure{Type: f.Type, Recipe: queueRecipes[f.Type], Error: f.Error, At: f.At}
			}
		}
	}
	items := make([]OverseerWorkItem, 0, len(rows))
	for _, r := range rows {
		slices.SortStableFunc(r.Sessions, func(a, b models.RunSession) int { return strings.Compare(b.StartedAt, a.StartedAt) })
		sort.Slice(r.Queued, func(i, j int) bool { return r.Queued[i].Rank < r.Queued[j].Rank })
		for _, s := range r.Sessions {
			r.UpdatedAt = maxString(r.UpdatedAt, s.StartedAt, s.EndedAt)
		}
		r.Section, r.Reason = workSection(r)
		items = append(items, *r)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].UpdatedAt != items[j].UpdatedAt {
			return items[i].UpdatedAt > items[j].UpdatedAt
		}
		return items[i].Key < items[j].Key
	})
	return items
}

// workSection is where a row goes, and why when it needs attention: a
// sandbox needs a person, a recipe's latest run failed, or a sandbox's pod
// was evicted or failed.
func workSection(r *OverseerWorkItem) (string, string) {
	if r.Kind == "agent" {
		return "agents", ""
	}
	for _, sb := range r.Sandboxes {
		if sb.RequiresHuman != "" {
			return "attention", "requires human: " + sb.RequiresHuman
		}
	}
	for _, sb := range r.Sandboxes {
		if sb.Pod == "evicted" || sb.Pod == "failed" {
			return "attention", fmt.Sprintf("%s's pod %s", sb.Name, sb.Pod)
		}
	}
	// A task the watch ran that failed, unless it is queued or running
	// again: its run may have succeeded in its sandbox.
	if f := r.Failure; f != nil && !slices.ContainsFunc(r.Queued, func(q OverseerQueuedTask) bool { return q.Type == f.Type }) {
		what := f.Recipe
		if what == "" {
			what = f.Type
		}
		return "attention", what + " failed"
	}
	// Sessions are newest first: the first of each recipe is its latest.
	seen := map[string]bool{}
	for _, s := range r.Sessions {
		if seen[s.Recipe] {
			continue
		}
		seen[s.Recipe] = true
		if s.Status == "failed" {
			return "attention", s.Recipe + " failed"
		}
	}
	for _, s := range r.Sessions {
		if s.Status == "running" {
			return "working", ""
		}
	}
	// A task held back by its cooldown is waiting to be queued.
	if len(r.Queued) > 0 || r.Cooldown != nil {
		return "queued", ""
	}
	return "idle", ""
}

func maxString(vals ...string) string {
	out := ""
	for _, v := range vals {
		if v > out {
			out = v
		}
	}
	return out
}
