package api

// Platform v2, phase 0: shadow read.
//
// These endpoints render the same world the v1 surfaces do, through the
// generic shapes of docs/design/platform-v2.md — Repo, Target, Recipe,
// Run — so the two UIs can be compared side by side on the same repo
// before any write path exists. Nothing here mutates anything: every
// handler is a GET, and the v2 UI disables its actions.
//
// Reuse is deliberate. Targets of kind issue/pr come from
// buildBoardWork, the same builder v1 renders, so attention is
// identical by construction rather than by reimplementation — if the
// two UIs disagree, the mapping is wrong, not the data.

import (
	"context"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/models"
	"github.com/google/go-github/v39/github"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// runGVR is v2's execution record.
var runGVR = schema.GroupVersionResource{
	Group: "board.gemini.google.com", Version: "v1alpha1", Resource: "runs",
}

// Target is the envelope every kind shares. The inbox view reads only
// these fields; kind-specific detail rides in Fields so a generic table
// can render a column without the client knowing what a verdict is.
type Target struct {
	ID              string            `json:"id"`   // issue:42, pr:1324, environment:sub-1
	Kind            string            `json:"kind"` // issue | pr | environment
	Title           string            `json:"title"`
	URL             string            `json:"url,omitempty"`
	Attention       string            `json:"attention,omitempty"` // needs-you | working | waiting
	AttentionReason string            `json:"attentionReason,omitempty"`
	LatestRun       *RunSummary       `json:"latestRun,omitempty"`
	Recipes         []RecipeAvailable `json:"recipes,omitempty"`
	Fields          map[string]any    `json:"fields,omitempty"`
	UpdatedAt       string            `json:"updatedAt,omitempty"`
}

// RunSummary is what a target carries about its most recent run. A Run
// object wins where one exists: it is the execution's own account,
// whereas receipts and sandbox annotations are inferences about it.
type RunSummary struct {
	Recipe  string `json:"recipe,omitempty"`
	Verdict string `json:"verdict,omitempty"`
	At      string `json:"at,omitempty"`
	Running bool   `json:"running,omitempty"`
	URL     string `json:"url,omitempty"` // receipt or artifact
	// Message is why, when a run ends badly. Without it a failure reads
	// as a red chip and the reason stays inside a pod.
	Message string `json:"message,omitempty"`
	// Sandbox and TaskDir locate the logs, so a row is a way in rather
	// than a dead end.
	Sandbox string `json:"sandbox,omitempty"`
	TaskDir string `json:"taskDir,omitempty"`
}

// RecipeAvailable answers "may I offer this verb here, and if not, why
// not" — so the UI can render a disabled button with its remedy
// instead of hiding a capability the user is looking for.
type RecipeAvailable struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	// Summary and Inputs ride along because a verb is rendered from this
	// envelope, not from the recipe catalogue. Without them the UI can
	// only draw a bare button: the reason `investigate` lost the topic
	// box it had in v1 was that its declared input stopped here.
	Summary string        `json:"summary,omitempty"`
	Inputs  []recipeInput `json:"inputs,omitempty"`
}

// ---------------------------------------------------------------------
// Recipes: the curated registry. Phase 0 ships it as a literal so the
// shapes can be argued with; the schema moves to data (in-tree YAML
// plus .repo-agent/recipes) when the executor lands.
// ---------------------------------------------------------------------

type recipeDef struct {
	Name     string              `json:"name"`
	Targets  []string            `json:"targets"`
	Summary  string              `json:"summary"`
	Inputs   []recipeInput       `json:"inputs,omitempty"`
	Verdicts []string            `json:"verdicts,omitempty"`
	Actions  map[string][]string `json:"actions,omitempty"`
	Needs    []string            `json:"needs,omitempty"` // precondition ids
}

type recipeInput struct {
	Name     string `json:"name"`
	Type     string `json:"type,omitempty"`
	Optional bool   `json:"optional,omitempty"`
	Hint     string `json:"hint,omitempty"`
}

var v2Recipes = []recipeDef{
	{Name: "understand", Targets: []string{"repo"}, Summary: "overview, architecture, code map",
		Verdicts: []string{"UPDATED", "NO-CHANGE"}},
	{Name: "catch-up", Targets: []string{"repo"}, Summary: "digest a recent window",
		Inputs: []recipeInput{{Name: "since", Hint: "2 weeks"}}},
	{Name: "investigate", Targets: []string{"repo"}, Summary: "a question, a subsystem, a comparison",
		Inputs: []recipeInput{{Name: "topic", Type: "text"}}},
	{Name: "draft-runbooks", Targets: []string{"repo"}, Summary: "draft or refresh the standard runbooks"},
	{Name: "author-runbook", Targets: []string{"repo"}, Summary: "create or update one runbook from a charter",
		Inputs: []recipeInput{{Name: "name"}, {Name: "charter", Type: "text", Optional: true}}},
	{Name: "triage", Targets: []string{"issue"}, Summary: "classify and label",
		Verdicts: []string{"TRIAGED"}, Actions: map[string][]string{"TRIAGED": {"plan", "fix"}}},
	{Name: "plan", Targets: []string{"issue"}, Summary: "propose an implementation plan",
		Verdicts: []string{"PLANNED"}, Actions: map[string][]string{"PLANNED": {"approve", "revise"}}},
	{Name: "fix", Targets: []string{"issue"}, Summary: "implement an approved plan as a PR",
		Inputs: []recipeInput{{Name: "instruction", Type: "text", Optional: true}},
		Needs:  []string{"verdict:PLANNED"},
		Actions: map[string][]string{
			"PR-OPENED": {"promote", "iterate", "review"}}},
	{Name: "review", Targets: []string{"pr"}, Summary: "draft a review, parked for you to submit",
		Verdicts: []string{"REVIEWED"}},
	{Name: "address-comments", Targets: []string{"pr"}, Summary: "answer unresolved review comments"},
	{Name: "fix-ci", Targets: []string{"pr"}, Summary: "diagnose and fix failing checks"},
	{Name: "iterate", Targets: []string{"pr"}, Summary: "change the PR with an instruction",
		Inputs: []recipeInput{{Name: "instruction", Type: "text"}}},
	{Name: "deploy", Targets: []string{"environment"}, Summary: "plan, then apply a runbook",
		Inputs: []recipeInput{{Name: "instance"}, {Name: "guidance", Type: "text", Optional: true}},
		Needs:  []string{"settings:gcp_project", "artifact:runbook"},
		Verdicts: []string{"PLANNED", "VERIFIED", "DEPLOYED-UNVERIFIED", "FAILED",
			"BLOCKED", "TORN-DOWN"},
		Actions: map[string][]string{
			"PLANNED":  {"apply", "discard"},
			"VERIFIED": {"reapply", "teardown"}}},
	{Name: "teardown", Targets: []string{"environment"}, Summary: "remove what a deployment created",
		Verdicts: []string{"TORN-DOWN", "PARTIAL", "BLOCKED"}},
}

// offer is how a recipe reaches a row: available until something says
// otherwise, and carrying everything the renderer needs to draw more
// than a bare button.
func offer(r recipeDef) RecipeAvailable {
	return RecipeAvailable{
		Name: r.Name, Available: true, Summary: r.Summary, Inputs: r.Inputs,
	}
}

func recipesForKind(kind string) []recipeDef {
	var out []recipeDef
	for _, r := range v2Recipes {
		for _, t := range r.Targets {
			if t == kind {
				out = append(out, r)
			}
		}
	}
	return out
}

func (s *Server) getV2Recipes(c *gin.Context) {
	kind := c.Query("kind")
	if kind == "" {
		c.JSON(http.StatusOK, v2Recipes)
		return
	}
	c.JSON(http.StatusOK, recipesForKind(kind))
}

// ---------------------------------------------------------------------
// Pages: the built-in layout, served in the same schema a repo could
// override. If our own page is hand-coded and users get a lesser YAML,
// the extension point is a fiction — so the default ships as data.
// ---------------------------------------------------------------------

func (s *Server) getV2Page(c *gin.Context) {
	if c.Param("page") != "repo" {
		c.JSON(http.StatusNotFound, gin.H{"error": "unknown page"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"page": "repo",
		"sections": []gin.H{
			{"id": "needs-you", "title": "Needs you", "view": "inbox",
				"source": gin.H{"targets": gin.H{"attention": "needs-you"}},
				"empty":  "✓ nothing needs you here"},
			{"id": "understanding", "title": "Understanding", "view": "doclist",
				"source": gin.H{"artifacts": "docs-exploration/**",
					"exclude": []string{"runbooks/", "runbook-deployments/", "studies/"}},
				"verbs": []string{"understand", "catch-up", "investigate"}},
			{"id": "runbooks", "title": "Runbooks", "view": "chips",
				"source": gin.H{"artifacts": "docs-exploration/runbooks/*"},
				"verbs":  []string{"draft-runbooks", "author-runbook"}},
			{"id": "environments", "title": "Environments", "view": "table",
				"source":  gin.H{"targets": gin.H{"kind": "environment"}},
				"columns": []string{"title", "verdict", "updated"},
				"verbs":   []string{"deploy"}},
			{"id": "activity", "title": "Activity", "view": "log",
				"source": gin.H{"runs": gin.H{"since": "24h"}}},
		},
	})
}

// ---------------------------------------------------------------------
// Targets
// ---------------------------------------------------------------------

func (s *Server) getV2Targets(c *gin.Context) {
	ctx := c.Request.Context()
	namespace := s.Auth.GetNamespaceFromContext(c)
	sessionUser := s.Auth.GetUserFromContext(c)
	board, member, err := s.resolveBoard(ctx, namespace, sessionUser, c.Param("repo"))
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Repo not accessible", "details": err.Error()})
		return
	}
	kindFilter := c.Query("kind")
	attentionFilter := c.Query("attention")

	targets := []Target{}

	// issue + pr: the same builder v1 renders, so attention matches by
	// construction rather than by a second implementation.
	if kindFilter == "" || kindFilter == "issue" || kindFilter == "pr" {
		items, ok := workFeedPeek(board.GetNamespace() + "/" + board.GetName())
		if !ok {
			if built, berr := s.buildBoardWork(ctx, board, member, namespace); berr == nil {
				workFeedPut(board.GetNamespace()+"/"+board.GetName(), built)
				items = built
			}
		}
		for i := range items {
			t := targetFromWorkItem(&items[i])
			if kindFilter != "" && t.Kind != kindFilter {
				continue
			}
			targets = append(targets, t)
		}
	}

	// environment: instance directories on the notes branch, joined with
	// their sandboxes — the same join the v1 runbook GET performs.
	if kindFilter == "" || kindFilter == "environment" {
		targets = append(targets, s.environmentTargets(c, board, member, namespace)...)
	}

	// Where a Run object exists it replaces the derived summary: v1
	// infers "running" from a sandbox annotation and a stage word, while
	// a Run is the executor's own record, carries the failure reason, and
	// names the task directory holding the logs.
	if byTarget := s.latestRunByTarget(ctx, namespace, board.GetName()); byTarget != nil {
		for i := range targets {
			if summary, ok := byTarget[targets[i].ID]; ok {
				targets[i].LatestRun = summary
			}
		}
	}

	if attentionFilter != "" {
		filtered := targets[:0]
		for _, t := range targets {
			if t.Attention == attentionFilter {
				filtered = append(filtered, t)
			}
		}
		targets = filtered
	}
	sort.SliceStable(targets, func(i, j int) bool { return targets[i].UpdatedAt > targets[j].UpdatedAt })
	c.JSON(http.StatusOK, targets)
}

func targetFromWorkItem(w *models.WorkItem) Target {
	kind := w.Type // issue | pr
	t := Target{
		ID:        kind + ":" + strconv.Itoa(w.Number),
		Kind:      kind,
		Title:     w.Title,
		URL:       w.HTMLURL,
		Attention: w.Attention,
		UpdatedAt: w.UpdatedAt,
		Fields: map[string]any{
			"number": w.Number,
			"stage":  w.Stage,
			"labels": w.Labels,
			"draft":  w.DraftPR,
		},
	}
	// Attention in v1 is a computed word; the reason is what the row
	// actually shows, so carry it explicitly rather than making the
	// client re-derive it from six booleans.
	switch {
	case w.Error != "":
		t.AttentionReason = "last agent run failed"
	case w.Plan != "" && !w.PlanApproved:
		t.AttentionReason = "plan awaiting approval"
	case w.Draft != "":
		t.AttentionReason = "draft awaiting your verdict"
	case w.ReviewRequested:
		t.AttentionReason = "your review is requested"
	case w.DraftPR:
		t.AttentionReason = "draft PR ready to promote"
	}
	if w.Stage != "" {
		t.LatestRun = &RunSummary{Verdict: w.Stage, At: w.UpdatedAt,
			Running: w.Sandbox != nil && strings.Contains(strings.ToLower(w.Stage), "ing")}
	}
	for _, r := range recipesForKind(kind) {
		avail := offer(r)
		for _, need := range r.Needs {
			if need == "verdict:PLANNED" && !w.PlanApproved {
				avail.Available, avail.Reason = false, "needs an approved plan"
			}
		}
		t.Recipes = append(t.Recipes, avail)
	}
	return t
}

// environmentTargets reads deployment instances from the notes branch
// and joins the sandbox that runs them.
func (s *Server) environmentTargets(c *gin.Context, board *unstructured.Unstructured, member, namespace string) []Target {
	ctx := c.Request.Context()
	out := []Target{}
	repoURL, _, _ := unstructured.NestedString(board.Object, "spec", "repoURL")
	_, repo, err := parseRepoURL(repoURL)
	if err != nil {
		return out
	}
	token, terr := s.memberToken(ctx, namespace)
	if terr != nil {
		return out
	}
	gh := githubClientForToken(ctx, token)
	ref := &github.RepositoryContentGetOptions{Ref: "exploration/notes"}
	_, dir, _, derr := gh.Repositories.GetContents(ctx, member, repo, "docs-exploration/runbook-deployments", ref)
	if derr != nil {
		return out
	}

	sandboxes := map[string]*unstructured.Unstructured{}
	if list, lerr := s.K8sManager.Client.Resource(k8s.SandboxGVR).Namespace(namespace).List(ctx, v1.ListOptions{
		LabelSelector: "sandbox.gemini.google.com/type=runbook",
	}); lerr == nil {
		for i := range list.Items {
			sb := &list.Items[i]
			inst := sb.GetAnnotations()["sandbox.gemini.google.com/runbook-instance"]
			if inst != "" {
				sandboxes[inst] = sb
			}
		}
	}

	for _, entry := range dir {
		if entry.GetType() != "dir" {
			continue
		}
		name := entry.GetName()
		t := Target{
			ID:     "environment:" + name,
			Kind:   "environment",
			Title:  name,
			URL:    entry.GetHTMLURL(),
			Fields: map[string]any{"instance": name},
		}
		// Verdict comes from the newest receipt — the same read the v1
		// runbook GET does, kept here so the client never learns that a
		// verdict is the first line of a file.
		if _, files, _, ferr := gh.Repositories.GetContents(ctx, member, repo, entry.GetPath(), ref); ferr == nil {
			newest := ""
			var newestFile *github.RepositoryContent
			for _, f := range files {
				if f.GetType() == "file" && strings.HasPrefix(f.GetName(), "receipt-") && f.GetName() > newest {
					newest, newestFile = f.GetName(), f
				}
			}
			if newestFile != nil {
				run := &RunSummary{Recipe: "deploy", At: receiptDate(newest), URL: newestFile.GetHTMLURL()}
				if rf, _, _, rerr := gh.Repositories.GetContents(ctx, member, repo, newestFile.GetPath(), ref); rerr == nil && rf != nil {
					if content, cerr := rf.GetContent(); cerr == nil {
						line, _, _ := strings.Cut(strings.TrimSpace(content), "\n")
						run.Verdict = strings.TrimSpace(line)
					}
				}
				t.LatestRun = run
				t.Fields["verdict"] = run.Verdict
			}
		}
		if sb, ok := sandboxes[name]; ok {
			ann := sb.GetAnnotations()
			t.Fields["sandbox"] = sb.GetName()
			if ann[annoTaskState] == "Running" {
				if t.LatestRun == nil {
					t.LatestRun = &RunSummary{}
				}
				t.LatestRun.Running = true
				t.Attention = "working"
			}
			t.UpdatedAt = ann["sandbox.gemini.google.com/last-task-time"]
		}
		for _, r := range recipesForKind("environment") {
			t.Recipes = append(t.Recipes, offer(r))
		}
		out = append(out, t)
	}
	return out
}

func receiptDate(name string) string {
	// receipt-20260924-0653.md → 2026-09-24T06:53:00Z
	parts := strings.Split(strings.TrimSuffix(name, ".md"), "-")
	if len(parts) < 3 {
		return ""
	}
	d, t := parts[1], parts[2]
	if len(d) != 8 || len(t) != 4 {
		return ""
	}
	return d[:4] + "-" + d[4:6] + "-" + d[6:8] + "T" + t[:2] + ":" + t[2:] + ":00Z"
}

// ---------------------------------------------------------------------
// Artifacts: dumb file access to the agent-written branch. Parsing
// belongs to targets; this endpoint only lists and reads.
// ---------------------------------------------------------------------

func (s *Server) getV2Artifacts(c *gin.Context) {
	ctx := c.Request.Context()
	namespace := s.Auth.GetNamespaceFromContext(c)
	sessionUser := s.Auth.GetUserFromContext(c)
	board, member, err := s.resolveBoard(ctx, namespace, sessionUser, c.Param("repo"))
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Repo not accessible"})
		return
	}
	repoURL, _, _ := unstructured.NestedString(board.Object, "spec", "repoURL")
	_, repo, perr := parseRepoURL(repoURL)
	if perr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Invalid repoURL"})
		return
	}
	token, terr := s.memberToken(ctx, namespace)
	if terr != nil {
		c.JSON(http.StatusOK, []gin.H{})
		return
	}
	gh := githubClientForToken(ctx, token)
	ref := &github.RepositoryContentGetOptions{Ref: "exploration/notes"}

	glob := c.DefaultQuery("glob", "docs-exploration/**")
	base := strings.TrimSuffix(strings.TrimSuffix(glob, "**"), "*")
	base = strings.TrimSuffix(base, "/")
	if base == "" || strings.Contains(base, "..") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "glob must be a path under docs-exploration"})
		return
	}
	recursive := strings.HasSuffix(glob, "**")

	out := []gin.H{}
	var walk func(p string, depth int)
	walk = func(p string, depth int) {
		_, dir, _, derr := gh.Repositories.GetContents(ctx, member, repo, p, ref)
		if derr != nil {
			return
		}
		for _, e := range dir {
			switch e.GetType() {
			case "file":
				out = append(out, gin.H{
					"path": e.GetPath(), "name": strings.TrimPrefix(e.GetPath(), base+"/"),
					"size": e.GetSize(), "htmlURL": e.GetHTMLURL(),
				})
			case "dir":
				if recursive && depth < 2 {
					walk(e.GetPath(), depth+1)
				}
			}
		}
	}
	walk(base, 0)
	sort.Slice(out, func(i, j int) bool { return out[i]["name"].(string) < out[j]["name"].(string) })
	c.JSON(http.StatusOK, out)
}

func (s *Server) getV2ArtifactContent(c *gin.Context) {
	p := c.Query("path")
	if !strings.HasPrefix(p, "docs-exploration/") || strings.Contains(p, "..") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "path must be under docs-exploration/"})
		return
	}
	// The v1 endpoint already implements this read with the same
	// constraints; phase 0 reuses it rather than duplicating the fetch.
	c.Params = append(c.Params, gin.Param{Key: "board", Value: c.Param("repo")})
	s.getBoardExplorationDoc(c)
}

// ---------------------------------------------------------------------
// Runs: phase 0 derives history from receipts, because v1 never created
// Run objects. Nothing is fabricated — each entry points at the
// artifact it was read from.
// ---------------------------------------------------------------------

func (s *Server) getV2Runs(c *gin.Context) {
	ctx := c.Request.Context()
	namespace := s.Auth.GetNamespaceFromContext(c)
	sessionUser := s.Auth.GetUserFromContext(c)
	board, member, err := s.resolveBoard(ctx, namespace, sessionUser, c.Param("repo"))
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Repo not accessible"})
		return
	}
	cutoff := sinceCutoff(c.Query("since"))

	runs := []gin.H{}
	// Real Run objects first — v2's own record of what it did.
	for _, r := range s.listRuns(ctx, namespace, board.GetName()) {
		if !cutoff.IsZero() && r.at.Before(cutoff) {
			continue
		}
		runs = append(runs, gin.H{
			"name": r.name, "recipe": r.summary.Recipe, "target": r.target,
			"verdict": r.summary.Verdict, "phase": r.phase, "message": r.summary.Message,
			"sandbox": r.summary.Sandbox, "taskDir": r.summary.TaskDir,
			"at": r.summary.At, "running": r.summary.Running, "derivedFrom": "run",
		})
	}
	for _, t := range s.environmentTargets(c, board, member, namespace) {
		if t.LatestRun == nil {
			continue
		}
		if at, err := time.Parse(time.RFC3339, t.LatestRun.At); err == nil &&
			!cutoff.IsZero() && at.Before(cutoff) {
			continue
		}
		runs = append(runs, gin.H{
			"recipe": t.LatestRun.Recipe, "target": t.ID,
			"verdict": t.LatestRun.Verdict, "at": t.LatestRun.At,
			"running": t.LatestRun.Running, "url": t.LatestRun.URL,
			"derivedFrom": "receipt",
		})
	}
	sort.Slice(runs, func(i, j int) bool {
		return runs[i]["at"].(string) > runs[j]["at"].(string)
	})
	c.JSON(http.StatusOK, runs)
}

// runRecord is one Run object, read once and reused: the Activity log
// and every target's latestRun come from the same list rather than two
// readings that can disagree.
type runRecord struct {
	name    string
	target  string
	phase   string
	at      time.Time
	summary RunSummary
}

func (s *Server) listRuns(ctx context.Context, namespace, boardName string) []runRecord {
	list, err := s.K8sManager.Client.Resource(runGVR).Namespace(namespace).List(ctx, v1.ListOptions{
		LabelSelector: "board.gemini.google.com/repo=" + boardName,
	})
	if err != nil {
		return nil
	}
	out := make([]runRecord, 0, len(list.Items))
	for i := range list.Items {
		r := &list.Items[i]
		recipe, _, _ := unstructured.NestedString(r.Object, "spec", "recipe")
		target, _, _ := unstructured.NestedString(r.Object, "spec", "target")
		phase, _, _ := unstructured.NestedString(r.Object, "status", "phase")
		verdict, _, _ := unstructured.NestedString(r.Object, "status", "verdict")
		msg, _, _ := unstructured.NestedString(r.Object, "status", "message")
		sandboxName, _, _ := unstructured.NestedString(r.Object, "status", "sandbox")
		taskDir, _, _ := unstructured.NestedString(r.Object, "status", "taskDir")
		started, _, _ := unstructured.NestedString(r.Object, "status", "startedAt")
		if started == "" {
			started = r.GetCreationTimestamp().UTC().Format(time.RFC3339)
		}
		// A phase is a fact about the machinery; a verdict is the
		// recipe's conclusion. Showing the phase when there is no
		// verdict beats showing nothing.
		if verdict == "" {
			verdict = phase
		}
		at, _ := time.Parse(time.RFC3339, started)
		out = append(out, runRecord{
			name: r.GetName(), target: target, phase: phase, at: at,
			summary: RunSummary{
				Recipe: recipe, Verdict: verdict, At: started,
				Running: phase == boardv1alpha1.RunPhaseRunning,
				Message: msg, Sandbox: sandboxName, TaskDir: taskDir,
			},
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].at.After(out[j].at) })
	return out
}

// latestRunByTarget keeps the newest Run per target, which is what a row
// shows. Returns nil when the repo has no Run objects, so callers keep
// their v1-derived state rather than blanking it.
func (s *Server) latestRunByTarget(ctx context.Context, namespace, boardName string) map[string]*RunSummary {
	records := s.listRuns(ctx, namespace, boardName)
	if len(records) == 0 {
		return nil
	}
	byTarget := map[string]*RunSummary{}
	for i := range records {
		if _, seen := byTarget[records[i].target]; seen {
			continue // records are newest-first
		}
		summary := records[i].summary
		byTarget[records[i].target] = &summary
	}
	return byTarget
}

// sinceCutoff reads a window like "24h" or "7d". An unparseable or
// absent window means no filter — a log that silently hides rows is
// worse than a long one.
func sinceCutoff(since string) time.Time {
	if since == "" {
		return time.Time{}
	}
	if strings.HasSuffix(since, "d") {
		if days, err := strconv.Atoi(strings.TrimSuffix(since, "d")); err == nil {
			return time.Now().Add(-time.Duration(days) * 24 * time.Hour)
		}
		return time.Time{}
	}
	d, err := time.ParseDuration(since)
	if err != nil {
		return time.Time{}
	}
	return time.Now().Add(-d)
}

// ---------------------------------------------------------------------
// Runs: the write path. Creating a Run is the only way v2 starts work,
// which is what makes "what is happening" answerable from one object.
// ---------------------------------------------------------------------

func (s *Server) createV2Run(c *gin.Context) {
	ctx := c.Request.Context()
	namespace := s.Auth.GetNamespaceFromContext(c)
	sessionUser := s.Auth.GetUserFromContext(c)
	board, _, err := s.resolveBoard(ctx, namespace, sessionUser, c.Param("repo"))
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Repo not accessible", "details": err.Error()})
		return
	}
	var req struct {
		Recipe string            `json:"recipe"`
		Target string            `json:"target"`
		Inputs map[string]string `json:"inputs"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Recipe == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "recipe is required"})
		return
	}
	if req.Target == "" {
		req.Target = "repo"
	}

	// A repo is driven by one platform. Refusing here rather than in the
	// reconciler means the UI gets an explanation instead of a Run that
	// fails a second later.
	platform, _, _ := unstructured.NestedString(board.Object, "spec", "platform")
	if platform != "v2" {
		c.JSON(http.StatusPreconditionFailed, gin.H{
			"error": "this repo is v1-managed; set spec.platform=v2 to run recipes here"})
		return
	}

	// The recipe must exist and accept this target kind — the same
	// availability rule the UI renders, enforced where it matters.
	kind := strings.SplitN(req.Target, ":", 2)[0]
	var def *recipeDef
	for i := range v2Recipes {
		if v2Recipes[i].Name == req.Recipe {
			def = &v2Recipes[i]
		}
	}
	if def == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown recipe " + req.Recipe})
		return
	}
	accepts := false
	for _, t := range def.Targets {
		if t == kind {
			accepts = true
		}
	}
	if !accepts {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": req.Recipe + " does not act on " + kind})
		return
	}

	// Required inputs are enforced here, not only in the form. A recipe
	// launched without its topic or its instance burns a sandbox to
	// discover what the declaration already knew.
	for _, in := range def.Inputs {
		if in.Optional {
			continue
		}
		if strings.TrimSpace(req.Inputs[in.Name]) == "" {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": req.Recipe + " needs " + in.Name})
			return
		}
	}

	run := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "board.gemini.google.com/v1alpha1",
		"kind":       "Run",
		"metadata": map[string]any{
			"generateName": req.Recipe + "-",
			"namespace":    namespace,
			"labels": map[string]any{
				"board.gemini.google.com/repo":   board.GetName(),
				"board.gemini.google.com/recipe": req.Recipe,
			},
		},
		"spec": map[string]any{
			"repo":      board.GetName(),
			"recipe":    req.Recipe,
			"target":    req.Target,
			"inputs":    toStringMap(req.Inputs),
			"requester": namespace,
		},
	}}
	created, cerr := s.K8sManager.Client.Resource(runGVR).Namespace(namespace).Create(ctx, run, v1.CreateOptions{})
	if cerr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not create run", "details": cerr.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"name": created.GetName(), "phase": "Pending"})
}

func toStringMap(in map[string]string) map[string]any {
	out := map[string]any{}
	for k, v := range in {
		if strings.TrimSpace(v) != "" {
			out[k] = v
		}
	}
	return out
}

// ---------------------------------------------------------------------
// Repo: identity plus the one fact the shadow UI must show loudly —
// which platform manages this repo.
// ---------------------------------------------------------------------

func (s *Server) getV2Repo(c *gin.Context) {
	ctx := c.Request.Context()
	namespace := s.Auth.GetNamespaceFromContext(c)
	sessionUser := s.Auth.GetUserFromContext(c)
	board, member, err := s.resolveBoard(ctx, namespace, sessionUser, c.Param("repo"))
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Repo not accessible"})
		return
	}
	repoURL, _, _ := unstructured.NestedString(board.Object, "spec", "repoURL")
	_, repo, _ := parseRepoURL(repoURL)
	platform, _, _ := unstructured.NestedString(board.Object, "spec", "platform")
	if platform == "" {
		platform = "v1"
	}
	engine, _, _ := unstructured.NestedString(board.Object, "spec", "sandbox", "engine")
	c.JSON(http.StatusOK, gin.H{
		"name": board.GetName(), "repo": repo, "forkOwner": member,
		"repoURL": repoURL, "platform": platform, "engine": engineOrDefault(engine),
		"branchURL": "https://github.com/" + path.Join(member, repo) + "/tree/exploration/notes",
		"readOnly":  platform != "v2",
	})
}
