package api

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/acpd"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/models"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/research"
)

// SessionRow is one row of the Sessions page (docs/design/sessions-view.md):
// one agent conversation in one sandbox, whatever started it, with the runs
// recorded into it and what its engine is doing now.
type SessionRow struct {
	Sandbox string `json:"sandbox"`
	// Overseer names the overseer whose sandbox this is, in
	// overseer-<name>: its routes are /api/overseers/<name>/…. Empty for
	// the member's own.
	Overseer string `json:"overseer,omitempty"`
	// Task is the conversation's session id, for
	// /api/task-sessions/<sandbox>/<task>. Empty on a requested row.
	Task string `json:"task,omitempty"`
	// Research is the research session id, for a research conversation's
	// rename and delete and for a requested row's pending view.
	Research string `json:"research,omitempty"`
	// Repo is owner/name, RepoURL the repository's URL.
	Repo    string `json:"repo,omitempty"`
	RepoURL string `json:"repoURL,omitempty"`
	// Board is the board the sandbox's work is on, when one is.
	Board string `json:"board,omitempty"`
	// Item is issue or pr, with Number and HTMLURL; empty for a session
	// about the repository (research).
	Item    string `json:"item,omitempty"`
	Number  int    `json:"number,omitempty"`
	HTMLURL string `json:"htmlURL,omitempty"`
	// Title is a research conversation's name, or the sandbox's title.
	Title string `json:"title,omitempty"`
	// Label is the newest run's recipe label, its recipe and session tag.
	Label   string `json:"label,omitempty"`
	Recipe  string `json:"recipe,omitempty"`
	Session string `json:"session,omitempty"`
	Engine  string `json:"engine,omitempty"`
	// Runs are the runs recorded into the conversation, newest first.
	Runs []models.RunSession `json:"runs,omitempty"`
	// Pod is the sandbox's pod: running, paused, starting, evicted or
	// failed (podState).
	Pod string `json:"pod"`
	// Live and the rest: researchSandboxView's fields of the same names.
	Live        bool              `json:"live,omitempty"`
	Busy        bool              `json:"busy,omitempty"`
	Waiting     bool              `json:"waiting,omitempty"`
	Retrying    *acpd.EngineRetry `json:"retrying,omitempty"`
	Held        bool              `json:"held,omitempty"`
	Unreachable string            `json:"unreachable,omitempty"`
	// Requested is a research session asked for whose sandbox does not
	// exist yet.
	Requested bool `json:"requested,omitempty"`
	// State is the row's section: needs-you, working, idle, paused, done
	// or failed (sessionState).
	State string `json:"state"`
	// UpdatedAt is when the newest run started or ended, RFC3339.
	UpdatedAt string `json:"updatedAt,omitempty"`
}

// getSessions lists every agent conversation in the member's namespace,
// newest change first: the runs recorded on each sandbox grouped by the
// conversation they ran in, the research sessions asked for and not made
// yet, and what each running pod's engine is doing. With them, unless
// ?overseers=false, the conversations of every overseer the member may
// see (overseerAllows), each row naming its overseer.
func (s *Server) getSessions(c *gin.Context) {
	ctx := c.Request.Context()
	namespace := s.Auth.GetNamespaceFromContext(c)
	list, err := s.K8sManager.Client.Resource(k8s.SandboxGVR).Namespace(namespace).List(ctx, v1.ListOptions{})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list sandboxes", "details": err.Error()})
		return
	}
	boards := sessionBoardsOf(s.visibleBoards(ctx, namespace, ""))
	// running is every namespace's dialable pods, by namespace/sandbox:
	// an overseer's sandbox may share its name with one of the member's.
	running := map[string]*corev1.Pod{}
	podsIn := func(ns string) map[string]*corev1.Pod {
		all, up := s.sandboxPods(ctx, ns)
		for name, pod := range up {
			running[ns+"/"+name] = pod
		}
		return all
	}

	var rows []SessionRow
	exists := map[string]bool{}
	pods := podsIn(namespace)
	for i := range list.Items {
		sb := &list.Items[i]
		if id := researchSessionOf(sb); id != "" {
			exists[id] = true
		}
		rows = append(rows, sandboxSessionRows(sb, boards, podState(sb, pods))...)
	}
	if c.Query("overseers") != "false" {
		rows = append(rows, s.overseerSessionRows(c, boards, podsIn)...)
	}
	s.attachSessionLiveState(ctx, rows, func(row SessionRow) *corev1.Pod {
		ns := namespace
		if row.Overseer != "" {
			ns = "overseer-" + row.Overseer
		}
		return running[ns+"/"+row.Sandbox]
	})

	requested, claimed := s.requestedResearchSessions(ctx, namespace, exists)
	for i := range rows {
		if rows[i].Title == "" && rows[i].Research != "" {
			rows[i].Title = claimed[rows[i].Research]
		}
		rows[i].State = sessionState(rows[i])
	}
	for _, view := range requested {
		row := SessionRow{
			Sandbox:   view.Sandbox,
			Research:  view.SessionID,
			RepoURL:   view.HTMLURL,
			Title:     view.Title,
			Label:     "Research",
			Recipe:    "research",
			Pod:       "starting",
			Requested: true,
			UpdatedAt: view.CreatedAt,
		}
		if owner, repo, err := parseRepoURL(view.HTMLURL); err == nil {
			row.Repo = owner + "/" + repo
			row.Board = boards.byRepo[strings.ToLower(row.Repo)]
		}
		row.State = sessionState(row)
		rows = append(rows, row)
	}
	slices.SortStableFunc(rows, func(a, b SessionRow) int {
		return cmp.Or(strings.Compare(b.UpdatedAt, a.UpdatedAt), strings.Compare(a.Sandbox, b.Sandbox), strings.Compare(a.Task, b.Task))
	})
	if rows == nil {
		rows = []SessionRow{}
	}
	c.JSON(http.StatusOK, gin.H{"sessions": rows})
}

// overseerSessionRows are the conversations in the sandboxes of every
// overseer the member may see, each naming its overseer. Board-less: an
// overseer's sandboxes are on no board, so their revises and drafts are
// the overseer routes', not a board's.
func (s *Server) overseerSessionRows(c *gin.Context, boards sessionBoards, podsIn func(string) map[string]*corev1.Pod) []SessionRow {
	ctx := c.Request.Context()
	overseers, err := s.K8sManager.ListOverseers(ctx)
	if err != nil {
		return nil
	}
	var rows []SessionRow
	for i := range overseers.Items {
		ov := &overseers.Items[i]
		if !s.overseerAllows(c, ov) {
			continue
		}
		ns := "overseer-" + ov.GetName()
		list, err := s.K8sManager.Client.Resource(k8s.SandboxGVR).Namespace(ns).List(ctx, v1.ListOptions{})
		if err != nil {
			continue
		}
		pods := podsIn(ns)
		for j := range list.Items {
			sb := &list.Items[j]
			if sb.GetName() == ns {
				continue // the overseer's own controller sandbox
			}
			for _, row := range sandboxSessionRows(sb, boards, podState(sb, pods)) {
				row.Overseer, row.Board = ov.GetName(), ""
				rows = append(rows, row)
			}
		}
	}
	return rows
}

// sessionBoards are the member's boards as the rows need them: each one's
// recipe catalog, every board's recipes together (for a sandbox on no
// board of theirs, an overseer's), and the board of each repository that
// has exactly one.
type sessionBoards struct {
	catalogs map[string][]boardv1alpha1.BoardRecipe
	all      []boardv1alpha1.BoardRecipe
	byRepo   map[string]string // lower-cased owner/name → board
}

func sessionBoardsOf(boards []unstructured.Unstructured) sessionBoards {
	out := sessionBoards{catalogs: map[string][]boardv1alpha1.BoardRecipe{}, byRepo: map[string]string{}}
	several := map[string]bool{}
	for i := range boards {
		board := &boards[i]
		catalog, _ := boardRecipes(board)
		out.catalogs[board.GetName()] = catalog
		for _, rec := range catalog {
			if !slices.ContainsFunc(out.all, func(r boardv1alpha1.BoardRecipe) bool { return r.Name == rec.Name }) {
				out.all = append(out.all, rec)
			}
		}
		repoURL, _, _ := unstructured.NestedString(board.Object, "spec", "repoURL")
		owner, repo, err := parseRepoURL(repoURL)
		if err != nil {
			continue
		}
		key := strings.ToLower(owner + "/" + repo)
		if _, seen := out.byRepo[key]; seen {
			several[key] = true
		}
		out.byRepo[key] = board.GetName()
	}
	// A repository on several boards names none of them, as fillSessionBoard.
	for key := range several {
		delete(out.byRepo, key)
	}
	return out
}

// sandboxRepo is the owner and name of the repository sb works on, from
// its htmlURL: an issue's, a PR's or the repository's own.
func sandboxRepo(annotations map[string]string) (string, string, bool) {
	if owner, repo, ok := itemRepo(annotations["htmlURL"]); ok {
		return owner, repo, true
	}
	owner, repo, err := parseRepoURL(annotations["htmlURL"])
	return owner, repo, err == nil
}

// sandboxSessionRows are sb's conversations: its recorded runs grouped by
// the session they ran in, newest run first in each. A sandbox that
// recorded none has none.
func sandboxSessionRows(sb *unstructured.Unstructured, boards sessionBoards, pod string) []SessionRow {
	annotations := sb.GetAnnotations()
	base := SessionRow{Sandbox: sb.GetName(), Pod: pod, Engine: sandboxEngine(annotations, "gemini"),
		Research: researchSessionOf(sb),
		Title:    cmp.Or(annotations[research.TitleAnnotation], annotations["title"])}
	if owner, repo, ok := sandboxRepo(annotations); ok {
		base.Repo = owner + "/" + repo
		base.RepoURL = fmt.Sprintf("https://github.com/%s/%s", owner, repo)
	}
	base.Board = cmp.Or(annotations[annoBoard], boards.byRepo[strings.ToLower(base.Repo)])
	switch w := workOf(sb); {
	case w.pr != 0:
		base.Item, base.Number = "pr", w.pr
		base.HTMLURL = fmt.Sprintf("%s/pull/%d", base.RepoURL, w.pr)
	case w.issue != 0:
		base.Item, base.Number = "issue", w.issue
		base.HTMLURL = fmt.Sprintf("%s/issues/%d", base.RepoURL, w.issue)
	}

	var rows []SessionRow
	index := map[string]int{}
	catalog := boards.catalogs[base.Board]
	if len(catalog) == 0 {
		catalog = boards.all
	}
	for _, run := range recordedRunSessions(sb, catalog) {
		if run.Recipe == "" {
			run.Recipe = factorycli.RunTaskType(run.Run)
			run.Label = run.Recipe
		}
		i, ok := index[run.Task]
		if !ok {
			i = len(rows)
			index[run.Task] = i
			row := base
			row.Task = run.Task
			rows = append(rows, row)
		}
		rows[i].Runs = append(rows[i].Runs, run)
	}
	for i := range rows {
		newest := rows[i].Runs[0]
		rows[i].Recipe, rows[i].Session = newest.Recipe, newest.Session
		rows[i].Label = cmp.Or(newest.Label, newest.Recipe)
		if rows[i].Recipe == "research" {
			rows[i].Label = "Research"
		} else {
			// The title and the research id are the research
			// conversation's; a sandbox's other sessions are named by
			// their recipe.
			rows[i].Title, rows[i].Research = annotations["title"], ""
		}
		rows[i].UpdatedAt = maxString(newest.StartedAt, newest.EndedAt)
	}
	return rows
}

// sessionState is the section a row is listed under.
func sessionState(row SessionRow) string {
	if row.Requested {
		return "working"
	}
	status := ""
	if len(row.Runs) > 0 {
		status = row.Runs[0].Status
	}
	switch {
	case row.Waiting || status == "ready":
		return "needs-you"
	case row.Busy || (status == "running" && row.Pod != "paused"):
		return "working"
	case status == "failed" || row.Pod == "failed" || row.Pod == "evicted":
		return "failed"
	case row.Pod == "paused":
		return "paused"
	case row.Live:
		return "idle"
	}
	return "done"
}

// sandboxPods are the namespace's sandbox pods by sandbox (podState's),
// and the running ones that can be dialed.
func (s *Server) sandboxPods(ctx context.Context, namespace string) (map[string]*corev1.Pod, map[string]*corev1.Pod) {
	all, running := map[string]*corev1.Pod{}, map[string]*corev1.Pod{}
	pods, err := s.K8sManager.Clientset.CoreV1().Pods(namespace).List(ctx, v1.ListOptions{LabelSelector: "sandbox"})
	if err != nil {
		return all, running
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.DeletionTimestamp != nil {
			continue
		}
		name := pod.Labels["sandbox"]
		if pod.Status.Phase == corev1.PodRunning && pod.Status.PodIP != "" {
			running[name] = pod
			all[name] = pod
		} else if all[name] == nil {
			all[name] = pod
		}
	}
	return all, running
}

// attachSessionLiveState asks each running pod's daemon (podOf a row, nil
// when none is up) what its rows' sessions are doing, in parallel and
// bounded as a whole: a list costs the slowest pod, not the sum, and a
// wedged one cannot stall the rest.
func (s *Server) attachSessionLiveState(ctx context.Context, rows []SessionRow, podOf func(SessionRow) *corev1.Pod) {
	ctx, cancel := context.WithTimeout(ctx, researchLiveTimeout)
	defer cancel()
	var wg sync.WaitGroup
	for i := range rows {
		pod := podOf(rows[i])
		if pod == nil || rows[i].Task == "" {
			continue
		}
		wg.Add(1)
		go func(row *SessionRow) {
			defer wg.Done()
			live := s.probeTaskSession(ctx, pod, row.Task)
			row.Live, row.Busy, row.Waiting = live.Live, live.Busy, live.Waiting
			row.Retrying, row.Held, row.Unreachable = live.Retrying, live.Held, live.Unreachable
		}(&rows[i])
	}
	wg.Wait()
}
