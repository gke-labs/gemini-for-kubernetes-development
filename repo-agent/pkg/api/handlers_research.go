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

package api

// Research sessions as the board lists them: the rail, renaming and
// deleting.
//
// Creating one goes through a Request on the board, because only the
// controller's image carries the factory CLI (see
// handlers_board_research.go). Talking to one does not: the conversation
// is the recipe's task session in the sandbox's daemon, and a row opens
// it as any task session is opened, by its sandbox and task
// (handlers_task_session.go).
//
// Rename and delete are keyed by session id alone, with no board and no
// sandbox name in the path. The sandbox carries the session's short id
// as a label precisely so it can be found that way, and the member's
// namespace comes from their session, so a caller cannot reach into
// another member's conversation by naming it.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/klog/v2"

	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/acpd"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/research"
)

// Labels and annotations factory stamps on a research sandbox. Mirrored
// rather than imported, like every other factory name in repo-agent:
// the two meet over HTTP and the CLI, never as one Go program.
const (
	// researchTypeLabel marks the sandbox kind. Research sandboxes are
	// deliberately their own type so the board's task-slot accounting
	// ignores them.
	researchTypeLabel = "sandbox.gemini.google.com/type"
	researchType      = "research"
	// researchSessionLabel holds the session's SHORT id — a label value
	// cannot hold a UUID's full charset budget alongside the rest, and
	// the short form is what the sandbox name is built from anyway.
	researchSessionLabel = "sandbox.gemini.google.com/research-session"
	// researchSessionIDAnnotation holds the full session id. It is the
	// authoritative match: the label is a digest prefix, so it narrows
	// the search but does not prove identity.
	researchSessionIDAnnotation = "sandbox.gemini.google.com/research-session-id"
)

// engineAPIKeySecretKey is the field of the member's factory-user Secret
// that holds the engine credential.
//
// Both engines acpd runs — gemini and antigravity — authenticate with the
// same Gemini key, so there is no engine switch here. An engine that
// needs another credential should add one rather than be quietly handed
// this key.
const engineAPIKeySecretKey = "GEMINI_API_KEY"

// safeResearchSessionID mirrors the controller's claim-key check. The id
// reaches acpd inside a URL path and names a Kubernetes label value, so
// it is validated before either.
var safeResearchSessionID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

// researchSandboxView is one session as the board sees it: what the
// sandbox object says, and — for the ones with a pod to ask — what the
// engine inside it is doing.
//
// The Kubernetes half comes first and stands alone, because the list has
// to render for paused, requested and still-booting sessions too, which
// are exactly the ones acpd cannot answer for. The live half is layered
// on afterwards and every field of it is optional: anything acpd does
// not say leaves the row as Kubernetes described it, which is a complete
// answer to "what sessions do I have" even when no pod is reachable.
type researchSandboxView struct {
	SessionID string `json:"sessionId"`
	// Task is the factory task whose agent session the conversation is:
	// `factory recipe research` records it on the sandbox, and the
	// conversation is the daemon's task session for it.
	Task string `json:"task,omitempty"`
	// Legacy is a sandbox with no recorded task: one the old research
	// path (the removed `factory research start`, acpd) made. Nothing here talks to
	// it any more; it is listed so it can be deleted.
	Legacy    bool   `json:"legacy,omitempty"`
	Sandbox   string `json:"sandbox"`
	Namespace string `json:"namespace"`
	Repo      string `json:"repo"`
	HTMLURL   string `json:"htmlUrl,omitempty"`
	CreatedAt string `json:"createdAt,omitempty"`
	// Engine is what the conversation runs on, as the sandbox was set up
	// for it — see acpd.ResearchEngine.
	Engine string `json:"engine,omitempty"`
	// Title is what the session is called: the canned exploration's
	// name, the topic it was started with, or the first thing the member
	// said. Empty until one of those has happened.
	Title string `json:"title,omitempty"`
	// Note is the file on the notes branch this session writes to,
	// fixed at its first save. Empty until then. Internal: the
	// browser never picks it, and a name the member has not been shown
	// is not one they can be surprised by.
	Note string `json:"-"`
	// Notes is the conversation's notes draft, for a recipe's: what its
	// last Save notes wrote, edits included. Internal: the conversation's
	// status reports it, with the clicks standing on it.
	Notes researchNotesDraft `json:"-"`
	// Requested marks a session that has been asked for but has no
	// sandbox yet: a standing claim on the board, not an object.
	Requested bool `json:"requested,omitempty"`
	// Paused is a sandbox scaled to zero: the conversation's transcript
	// survives on the PVC but the engine is gone, so resuming means a
	// fresh session over the same history.
	Paused bool `json:"paused"`
	// Starting is a sandbox whose pod is not running yet. The object
	// exists minutes before the conversation can be had — an image to
	// pull, a PVC to bind, a repo to clone — and reporting that as up
	// sends people to a session that cannot answer.
	Starting bool `json:"starting,omitempty"`
	// Live says acpd has a session for this row: an engine is running and
	// the conversation can be had right now. False for a running pod
	// nobody has opened yet, which is the ordinary resting state.
	Live bool `json:"live,omitempty"`
	// Busy is a turn in flight, and Waiting narrows it to a turn that has
	// stopped on a permission request. Waiting implies Busy.
	Busy    bool `json:"busy,omitempty"`
	Waiting bool `json:"waiting,omitempty"`
	// Held is a task session whose task is still running (the recipe's
	// start, asking the opening question): it can be watched, not driven.
	Held bool `json:"held,omitempty"`
	// Unreachable is why the pod's acpd could not be asked. The row still
	// renders from what Kubernetes said — the sandbox is the durable
	// thing and the conversation survives a daemon that is briefly not
	// answering — but the live fields above mean nothing when this is set,
	// and a list that quietly showed them as false would be reporting an
	// idle session when what it found was a broken one.
	Unreachable string `json:"unreachable,omitempty"`
	// Pod is nil until the pod is running. Its presence is what
	// "reachable" means for every other call here.
	Pod *corev1.Pod `json:"-"`
}

// acpSession is the conversation's id on the daemon: the recipe's task.
func (v researchSandboxView) acpSession() string {
	return v.Task
}

// researchClient reaches the daemon's task sessions, which host the
// conversation. False when the sandbox's image keeps none.
func (s *Server) researchClient(ctx context.Context, pod *corev1.Pod) (*acpd.Client, bool) {
	return taskSessionClientForPod(ctx, s.ACPD, pod)
}

// researchViewFromSandbox reads one sandbox object, or reports false if
// it is not a research sandbox with a session id on it.
func researchViewFromSandbox(sb *unstructured.Unstructured) (researchSandboxView, bool) {
	annotations := sb.GetAnnotations()
	sessionID := annotations[researchSessionIDAnnotation]
	if sessionID == "" {
		// A research sandbox with no session id cannot be addressed:
		// every route here starts from the id. Skipping it keeps a
		// hand-made or half-written object out of the list rather than
		// showing a row nothing can open.
		return researchSandboxView{}, false
	}
	view := researchSandboxView{
		SessionID: sessionID,
		Task:      factorycli.ResearchTask(annotations),
		Sandbox:   sb.GetName(),
		Namespace: sb.GetNamespace(),
		Repo:      annotations["repo"],
		HTMLURL:   annotations["htmlURL"],
		Engine:    acpd.ResearchEngine(annotations),
		Title:     annotations[research.TitleAnnotation],
		Note:      annotations[research.NoteAnnotation],
	}
	view.Legacy = view.Task == ""
	view.Notes = researchNotesDraft{
		Markdown:  annotations[annoNotesDraft],
		DraftedAt: annotations[annoNotesDraftedAt],
		SavedAt:   annotations[annoNotesSaved],
	}
	if ts := sb.GetCreationTimestamp(); !ts.IsZero() {
		view.CreatedAt = ts.UTC().Format(time.RFC3339)
	}
	if replicas, found, _ := unstructured.NestedInt64(sb.Object, "spec", "replicas"); found && replicas == 0 {
		view.Paused = true
	}
	return view, true
}

// getResearchSessions lists the member's research sessions: every
// research sandbox, plus the ones that have been asked for and do not
// exist yet.
//
// The pending rows matter more here than in any other list. A sandbox
// is minutes away — image pull, PVC, clone — and a member who clicked
// "overview" and saw nothing appear would reasonably click again, which
// costs another sandbox and another engine.
func (s *Server) getResearchSessions(c *gin.Context) {
	ctx := c.Request.Context()
	namespace := s.Auth.GetNamespaceFromContext(c)

	list, err := s.K8sManager.Client.Resource(k8s.SandboxGVR).Namespace(namespace).List(ctx, v1.ListOptions{
		LabelSelector: researchTypeLabel + "=" + researchType,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list research sandboxes", "details": err.Error()})
		return
	}

	running := s.runningSandboxPods(ctx, namespace)
	views := []researchSandboxView{}
	exists := map[string]bool{}
	for i := range list.Items {
		if view, ok := researchViewFromSandbox(&list.Items[i]); ok {
			view.Pod = running[view.Sandbox]
			view.Starting = !view.Paused && view.Pod == nil
			views = append(views, view)
			exists[view.SessionID] = true
		}
	}
	s.attachResearchLiveState(ctx, views)
	requested, claimed := s.requestedResearchSessions(ctx, namespace, exists)
	views = append(views, requested...)
	// Fill an untitled sandbox from the claim that asked for it.
	//
	// `factory recipe research` creates the Sandbox object early and then
	// clones for minutes, while the title is stamped onto it by the
	// controller in the pass that first notices it exists. Those are
	// different moments, and between them the row has a sandbox (so the
	// claim is hidden as served) and no annotation (so it has no name) —
	// which is how a session that was just called "Changes in the last
	// 2 weeks" turns into "untitled" and back again a minute later.
	for i := range views {
		if views[i].Title == "" {
			views[i].Title = claimed[views[i].SessionID]
		}
	}
	// Newest first: a session list is read from the top, and the one you
	// just started is the one you want.
	sort.Slice(views, func(i, j int) bool {
		if views[i].CreatedAt != views[j].CreatedAt {
			return views[i].CreatedAt > views[j].CreatedAt
		}
		return views[i].Sandbox < views[j].Sandbox
	})
	// forkOwner is the member's GitHub login, which is also the owner of
	// the fork research notes are pushed to. The panel needs it only to
	// build the footnote link to that branch; it is not per-session.
	//
	// The branch name is sent rather than written into the page, so the
	// Go constant the write path has to agree with is the only copy of
	// the string.
	c.JSON(http.StatusOK, gin.H{
		"sessions":    views,
		"forkOwner":   s.Auth.GetUserFromContext(c),
		"notesBranch": notesBranch,
	})
}

// requestedResearchSessions reads the standing research claims off the
// member's boards and renders them as rows.
//
// Read from the boards rather than remembered here because the API
// server is stateless and replicated: the claim on the board is the
// only record of a click between the POST and the sandbox appearing.
// A board that cannot be read is skipped rather than failing the list —
// the sessions that DO exist are the more important half of the answer.
//
// Returns the rows, and separately the title every claim carries —
// including the ones whose sandbox has arrived, which get no row. Those
// titles are the only copy there is until the controller stamps the
// sandbox, and the caller needs them to keep a name on the row in the
// meantime.
func (s *Server) requestedResearchSessions(ctx context.Context, namespace string, exists map[string]bool) ([]researchSandboxView, map[string]string) {
	requests, err := s.listRequests(ctx, namespace, v1.ListOptions{
		LabelSelector: boardv1alpha1.LabelVerb + "=" + boardv1alpha1.VerbResearch,
	})
	if err != nil {
		klog.V(2).Infof("research: cannot list requests for pending sessions in %s: %v", namespace, err)
		return nil, nil
	}
	// The repo a session belongs to is the board's, so the boards still
	// have to be read — but only the ones something was actually asked
	// of, and only once each.
	repos := map[string][2]string{}
	repoOf := func(boardName string) (repo, repoURL string) {
		if cached, ok := repos[boardName]; ok {
			return cached[0], cached[1]
		}
		board, err := s.K8sManager.Client.Resource(repoBoardGVR).Namespace(namespace).Get(ctx, boardName, v1.GetOptions{})
		if err != nil {
			repos[boardName] = [2]string{}
			return "", ""
		}
		repoURL, _, _ = unstructured.NestedString(board.Object, "spec", "repoURL")
		_, repo, _ = parseRepoURL(repoURL)
		repos[boardName] = [2]string{repo, repoURL}
		return repo, repoURL
	}

	var out []researchSandboxView
	titles := map[string]string{}
	for i := range requests {
		req := &requests[i]
		spec := req.Spec.Research
		if spec == nil || req.Spec.Member != namespace || !safeResearchSessionID.MatchString(spec.SessionID) {
			continue
		}
		kickoff := research.Kickoff{Kind: spec.Kind, Topic: spec.Topic, Since: spec.Since, Title: spec.Title}
		if title := kickoff.ResolvedTitle(); title != "" {
			titles[spec.SessionID] = title
		}
		// A request whose sandbox has arrived is settled, or about to
		// be; showing both would double the row. Its title is taken
		// first, above: the sandbox it was served by does not
		// necessarily carry one yet.
		if exists[spec.SessionID] || !req.Active() {
			continue
		}
		repo, repoURL := repoOf(req.Spec.Board)
		out = append(out, researchSandboxView{
			SessionID: spec.SessionID,
			Sandbox:   factorycli.ResearchSandboxName(repo, spec.SessionID),
			Namespace: namespace,
			Repo:      repo,
			HTMLURL:   repoURL,
			CreatedAt: req.CreationTimestamp.UTC().Format(time.RFC3339),
			Title:     kickoff.ResolvedTitle(),
			Requested: true,
		})
	}
	return out, titles
}

// findResearchSandbox locates the sandbox hosting one session.
//
// The label narrows the list server-side; the annotation then confirms
// it. Both are needed: the label holds only eight hex characters of a
// digest, so it is a filter, not proof.
func (s *Server) findResearchSandbox(ctx context.Context, namespace, sessionID string) (researchSandboxView, bool, error) {
	list, err := s.K8sManager.Client.Resource(k8s.SandboxGVR).Namespace(namespace).List(ctx, v1.ListOptions{
		LabelSelector: researchSessionLabel + "=" + factorycli.ResearchShortID(sessionID),
	})
	if err != nil {
		return researchSandboxView{}, false, err
	}
	for i := range list.Items {
		view, ok := researchViewFromSandbox(&list.Items[i])
		if ok && view.SessionID == sessionID {
			return view, true, nil
		}
	}
	return researchSandboxView{}, false, nil
}

// runningSandboxPods maps the sandboxes in the namespace that have a
// running pod behind them to that pod's IP.
//
// One list for the whole page rather than a lookup per row: a member
// with a dozen sessions should not cost a dozen pod lists every ten
// seconds. A failure is reported as "nothing is running", which reads as
// starting — the honest answer when the pods cannot be seen at all.
//
// The pod comes back rather than a bare yes, because the caller's next
// question is always "so what is it doing", and that is asked of the pod.
// Re-deriving it per row would be researchPod once per session — the
// list this function exists to avoid.
func (s *Server) runningSandboxPods(ctx context.Context, namespace string) map[string]*corev1.Pod {
	running := map[string]*corev1.Pod{}
	pods, err := s.K8sManager.Clientset.CoreV1().Pods(namespace).List(ctx, v1.ListOptions{LabelSelector: "sandbox"})
	if err != nil {
		klog.V(2).Infof("research: cannot list sandbox pods in %s: %v", namespace, err)
		return running
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.DeletionTimestamp == nil && pod.Status.Phase == corev1.PodRunning && pod.Status.PodIP != "" {
			running[pod.Labels["sandbox"]] = pod
		}
	}
	return running
}

// researchLiveTimeout bounds the whole fan-out below.
//
// Short because this is a poll, not a page load: the list refreshes every
// ten seconds, so a pod that cannot answer in a couple of seconds has
// nothing to say that will not still be true next time. The cost of
// waiting longer is paid by every other row, which is the wrong trade —
// a member with nine healthy sessions and one wedged pod should not have
// the whole rail stall on the wedged one.
const researchLiveTimeout = 2 * time.Second

// attachResearchLiveState fills in what each session's engine is doing,
// for the rows that have a running pod to ask.
//
// This is the one place the list leaves Kubernetes. It is worth it
// because the alternative is a rail that says "up" for everything, where
// "up" means the pod is running — which is not a question anybody has.
// What they want to know is which conversation is working, which has
// stopped to ask them something, and which is simply sitting there.
//
// Asked in parallel and bounded as a whole. One request per running pod
// sounds like a lot until you count them: it is one small HTTP call
// inside the cluster per session the member actually has open, every ten
// seconds, and they are concurrent, so the list costs the slowest pod
// rather than the sum of all of them.
//
// Nothing here can fail the list. A pod that does not answer gets its
// reason recorded on its own row and the rest of the page renders — the
// sandboxes are the durable thing, and they are still there whether or
// not a daemon inside one is talking.
func (s *Server) attachResearchLiveState(ctx context.Context, views []researchSandboxView) {
	ctx, cancel := context.WithTimeout(ctx, researchLiveTimeout)
	defer cancel()

	var wg sync.WaitGroup
	for i := range views {
		if views[i].Pod == nil || views[i].Legacy {
			continue
		}
		wg.Add(1)
		// By pointer into the slice: these are distinct elements, so the
		// writes do not race, and the alternative — collecting into a map
		// and merging — is a second pass for nothing.
		go func(view *researchSandboxView) {
			defer wg.Done()
			client, ok := s.researchClient(ctx, view.Pod)
			if !ok {
				view.Unreachable = "the sandbox's image keeps no agent sessions for its tasks"
				return
			}
			session, err := client.GetSession(ctx, view.acpSession())
			switch {
			case err == nil:
				view.Live = true
				view.Busy = session.Busy
				view.Waiting = session.Waiting
				view.Held = session.Held
			case errors.Is(err, acpd.ErrNotFound):
				// A running pod with no session loaded: the resting state of
				// every conversation nobody has opened since its start
				// ended, and of every one whose daemon has restarted. Not
				// an error, and not unreachable either — the daemon
				// answered.
			default:
				view.Unreachable = err.Error()
			}
		}(&views[i])
	}
	wg.Wait()
}

// researchPod returns the sandbox's running pod, or nil when there is
// none to dial yet.
func (s *Server) researchPod(ctx context.Context, namespace, sandboxName string) (*corev1.Pod, error) {
	pods, err := s.K8sManager.Clientset.CoreV1().Pods(namespace).List(ctx, v1.ListOptions{
		LabelSelector: "sandbox=" + sandboxName,
	})
	if err != nil {
		return nil, err
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		// Only a running pod with an IP: an address taken from a pending
		// or terminating pod fails on connect, which looks like acpd
		// being broken rather than the sandbox not being up.
		if pod.DeletionTimestamp == nil && pod.Status.Phase == corev1.PodRunning && pod.Status.PodIP != "" {
			return pod, nil
		}
	}
	return nil, nil
}

// engineAPIKey reads the member's engine credential.
//
// Read fresh on every session create rather than cached: acpd holds the
// key only until the engine child is spawned and drops it, so this is
// the only place it lives, and it must be re-supplied after an acpd
// restart. Caching it here would be a second copy for no gain.
func (s *Server) engineAPIKey(ctx context.Context, namespace string) (string, error) {
	secret, err := s.K8sManager.Clientset.CoreV1().Secrets(namespace).Get(ctx, "factory-user", v1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("reading the factory-user secret: %w", err)
	}
	key := string(secret.Data[engineAPIKeySecretKey])
	if key == "" {
		return "", fmt.Errorf("no %s in this namespace's factory-user secret", engineAPIKeySecretKey)
	}
	return key, nil
}

// researchError maps an acpd failure onto a status for the caller.
//
// A pod that has gone away mid-conversation surfaces as a dial error,
// not an HTTP status, and reporting that as 500 would tell the UI to
// show a crash when the honest answer is that the session is over.
func researchError(c *gin.Context, err error) {
	var acpErr *acpd.Error
	if errors.As(err, &acpErr) {
		c.JSON(acpErr.StatusCode, gin.H{"error": acpErr.Message})
		return
	}
	c.JSON(http.StatusBadGateway, gin.H{"error": "the conversation server is unreachable", "details": err.Error()})
}

// renameResearchSession sets what a session is called.
//
// It needs no pod: renaming a paused or still-booting session is
// reasonable, and neither has one to dial.
func (s *Server) renameResearchSession(c *gin.Context) {
	ctx := c.Request.Context()
	namespace := s.Auth.GetNamespaceFromContext(c)
	sessionID := c.Param("session")
	if !safeResearchSessionID.MatchString(sessionID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid session id"})
		return
	}
	var req struct {
		Title string `json:"title"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "title is required"})
		return
	}
	// Truncated rather than rejected: the member pasted something long
	// and meant it as a name.
	title := research.Truncate(req.Title)
	if title == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "title is required"})
		return
	}
	view, found, err := s.findResearchSandbox(ctx, namespace, sessionID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to look up the session", "details": err.Error()})
		return
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "research session not found"})
		return
	}
	if err := s.setResearchTitle(ctx, view.Namespace, view.Sandbox, title); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to rename the session", "details": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"sessionId": sessionID, "title": title})
}

// setResearchTitle writes the title annotation onto the sandbox.
//
// On the sandbox rather than in a table because the sandbox is the
// session: deleting it must take the name with it, and nothing else
// here has a database.
func (s *Server) setResearchTitle(ctx context.Context, namespace, sandboxName, title string) error {
	sandboxes := s.K8sManager.Client.Resource(k8s.SandboxGVR).Namespace(namespace)
	sb, err := sandboxes.Get(ctx, sandboxName, v1.GetOptions{})
	if err != nil {
		return err
	}
	annotations := sb.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	if annotations[research.TitleAnnotation] == title {
		return nil
	}
	annotations[research.TitleAnnotation] = title
	// A rename unpins the note. The name is what the note is saved as,
	// so a member who renames a session and then saves expects the file
	// to be called what the session is now called — the pin exists to
	// keep repeated saves landing on one file, not to outlive the name
	// it was derived from. Whatever was already pushed under the old
	// name stays on the branch.
	delete(annotations, research.NoteAnnotation)
	sb.SetAnnotations(annotations)
	_, err = sandboxes.Update(ctx, sb, v1.UpdateOptions{})
	return err
}

// deleteResearchSession ends a conversation for good.
//
// It deletes the sandbox, not the acpd session, because the sandbox IS
// the session: the transcript lives on its PVC and nothing is kept
// anywhere else. Deleting the acpd session alone would stop the engine
// and leave a sandbox that costs a PVC and answers no questions.
func (s *Server) deleteResearchSession(c *gin.Context) {
	ctx := c.Request.Context()
	namespace := s.Auth.GetNamespaceFromContext(c)
	sessionID := c.Param("session")
	if !safeResearchSessionID.MatchString(sessionID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid session id"})
		return
	}
	// Resolved from the object alone, with no pod: a paused or
	// half-booted session is exactly the one a member most wants to be
	// able to delete.
	view, found, err := s.findResearchSandbox(ctx, namespace, sessionID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to look up the session", "details": err.Error()})
		return
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "research session not found"})
		return
	}
	if err := s.K8sManager.DeleteSandbox(ctx, namespace, view.Sandbox); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "delete failed", "details": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}

// Frames sent to the browser on the event websocket.
const (
	researchFrameOpen   = "open"
	researchFrameEvent  = "event"
	researchFrameClosed = "closed"
)

// researchFrame is one message on the event websocket.
//
// Every event frame carries the offset AFTER it, so a browser that
// stores the last one it saw and reconnects with ?offset= sees each
// event exactly once across a dropped connection. That is the whole
// resumption contract; there is no sequence number to reconcile.
type researchFrame struct {
	Type    string        `json:"type"`
	Offset  int64         `json:"offset"`
	Event   *acpd.Event   `json:"event,omitempty"`
	Session *acpd.Session `json:"session,omitempty"`
	Reason  string        `json:"reason,omitempty"`
	Error   string        `json:"error,omitempty"`
}

const (
	researchPingInterval = 20 * time.Second
	researchReadTimeout  = 70 * time.Second
)

// eventOffset is the ?offset= to follow events from, answering 400 when
// it is not one.
func eventOffset(c *gin.Context) (int64, bool) {
	raw := c.Query("offset")
	if raw == "" {
		return 0, true
	}
	offset, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || offset < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid offset"})
		return 0, false
	}
	return offset, true
}

// errQuietClose ends an event stream with a closed frame that carries no
// error.
var errQuietClose = errors.New("closed quietly")

// followSession sends a session's events down ws from offset, after the
// open frame for the session ensure returns. It returns when the stream
// ends or the browser goes; cancel ends ctx.
func followSession(ctx context.Context, cancel context.CancelFunc, ws *websocket.Conn, client *acpd.Client, sessionID string, offset int64, ensure func(context.Context) (*acpd.Session, error)) {
	out := &wsJSON{ws: ws}

	session, err := ensure(ctx)
	if err != nil {
		frame := researchFrame{Type: researchFrameClosed}
		if !errors.Is(err, errQuietClose) {
			frame.Error = err.Error()
		}
		_ = out.send(frame)
		return
	}
	if err := out.send(researchFrame{Type: researchFrameOpen, Session: session, Offset: offset}); err != nil {
		return
	}

	// Keepalive, and the reader that keeps the read deadline alive. The
	// browser sends nothing here, so without this loop the socket would
	// be closed by its own read timeout while the agent was working.
	_ = ws.SetReadDeadline(time.Now().Add(researchReadTimeout))
	ws.SetPongHandler(func(string) error {
		return ws.SetReadDeadline(time.Now().Add(researchReadTimeout))
	})
	go func() {
		defer cancel()
		for {
			if _, _, rerr := ws.ReadMessage(); rerr != nil {
				return
			}
			_ = ws.SetReadDeadline(time.Now().Add(researchReadTimeout))
		}
	}()
	go func() {
		ticker := time.NewTicker(researchPingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := out.ping(); err != nil {
					cancel()
					return
				}
			}
		}
	}()

	// Events rather than Follow: Follow hands the callback an event but
	// not the offset after it, and the offset is what makes a reconnect
	// exact.
	stream, err := client.Events(ctx, sessionID, offset, true)
	if err != nil {
		_ = out.send(researchFrame{Type: researchFrameClosed, Offset: offset, Error: err.Error()})
		return
	}
	defer func() { _ = stream.Close() }()

	for stream.Next() {
		event := stream.Event()
		if err := out.send(researchFrame{Type: researchFrameEvent, Offset: stream.Offset(), Event: &event}); err != nil {
			return
		}
	}
	if ctx.Err() != nil {
		// The browser went away, or the ping failed. Nothing to report:
		// there is nobody left to report it to.
		return
	}
	frame := researchFrame{Type: researchFrameClosed, Offset: stream.Offset(), Reason: "stream ended"}
	if err := stream.Err(); err != nil {
		frame.Error = err.Error()
	}
	_ = out.send(frame)
}

// wsJSON serializes frames onto one websocket.
//
// The mutex is not optional: the ping goroutine and the event loop both
// write, and gorilla/websocket permits exactly one concurrent writer.
type wsJSON struct {
	mu sync.Mutex
	ws *websocket.Conn
}

func (w *wsJSON) send(frame researchFrame) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ws.WriteJSON(frame)
}

func (w *wsJSON) ping() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
}
