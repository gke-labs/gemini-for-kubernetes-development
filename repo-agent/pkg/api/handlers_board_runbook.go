package api

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/sandbox"
	"github.com/google/go-github/v39/github"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// removeRunbookInstance deletes a dead instance's records — its
// directory on the fork branch. Receipts are the audit log, so this is
// owner-initiated only, for instances whose story is over (torn down,
// failed, superseded); it never touches cloud resources.
func (s *Server) removeRunbookInstance(c *gin.Context) {
	ctx := c.Request.Context()
	namespace := s.Auth.GetNamespaceFromContext(c)
	sessionUser := s.Auth.GetUserFromContext(c)
	board, member, err := s.resolveBoard(ctx, namespace, sessionUser, c.Param("board"))
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Board not accessible", "details": err.Error()})
		return
	}
	repoURL, _, _ := unstructured.NestedString(board.Object, "spec", "repoURL")
	_, repo, err := parseRepoURL(repoURL)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Invalid repoURL on board"})
		return
	}
	instance := c.Param("instance")
	if instance == "" || strings.ContainsAny(instance, "/.") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid instance name"})
		return
	}
	token, terr := s.memberToken(ctx, namespace)
	if terr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "No member token"})
		return
	}
	gh := githubClientForToken(ctx, token)
	ref := runsBranch
	opts := &github.RepositoryContentGetOptions{Ref: ref}
	// Every layout the list reads, not the one this handler was written
	// against. Runs moved to agent-runs/ and a legacy run is adopted
	// into it only when something touches it, so the row on screen may
	// have come from any of the three — removing from a guessed path
	// deletes nothing and the run stays listed.
	var files []*github.RepositoryContent
	found := false
	for _, base := range append([]string{runsPath}, legacyRunPaths...) {
		inBase, ok := runFilePaths(ctx, gh, member, repo, base+"/"+instance, opts)
		if !ok {
			continue
		}
		found = true
		files = append(files, inBase...)
	}
	if !found {
		// A local-only run's records are the sandbox's: removing them is
		// deleting the sandbox, which takes its teardown script with it.
		if sb, _ := s.runSandboxFor(ctx, namespace, repo, instance); sb != "" {
			c.JSON(http.StatusConflict, gin.H{"error": "run " + instance + " is local-only: its records live in sandbox " + sb + ", and are removed by deleting that sandbox"})
			return
		}
		c.JSON(http.StatusNotFound, gin.H{"error": "instance records not found"})
		return
	}
	msg := "remove records for retired instance " + instance
	for _, f := range files {
		sha := f.GetSHA()
		if _, _, ferr := gh.Repositories.DeleteFile(ctx, member, repo, f.GetPath(), &github.RepositoryContentFileOptions{
			Message: &msg, SHA: &sha, Branch: &ref,
		}); ferr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed deleting " + f.GetName(), "details": ferr.Error()})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"status": "removed", "instance": instance, "files": len(files)})
}

// runFilePaths lists every file under dir, descending into
// subdirectories, and reports whether dir exists at all.
//
// The Contents API deletes one file at a time and will not remove a
// directory, so a run that has grown a subtree has to be enumerated in
// full: one file left behind keeps the directory alive, and a live
// directory keeps the run in the list — the same silent half-removal
// the caller is trying to fix.
func runFilePaths(ctx context.Context, gh *github.Client, member, repo, dir string, opts *github.RepositoryContentGetOptions) ([]*github.RepositoryContent, bool) {
	_, entries, _, err := gh.Repositories.GetContents(ctx, member, repo, dir, opts)
	if err != nil {
		return nil, false
	}
	var files []*github.RepositoryContent
	for _, e := range entries {
		switch e.GetType() {
		case "file":
			files = append(files, e)
		case "dir":
			if sub, ok := runFilePaths(ctx, gh, member, repo, e.GetPath(), opts); ok {
				files = append(files, sub...)
			}
		}
	}
	return files, true
}

// repoShortName mirrors factory's shortName: initials of hyphenated
// repos (in-cluster-storage → ics), else the name truncated. It is the
// first half of RUNBOOK_RESOURCE_PREFIX — shown in the UI beside the
// instance-name input so users never hand-type the repo into a name.
func repoShortName(repo string) string {
	slug := strings.ToLower(repo)
	clean := make([]rune, 0, len(slug))
	for _, r := range slug {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			clean = append(clean, r)
		} else {
			clean = append(clean, '-')
		}
	}
	slug = strings.Trim(string(clean), "-")
	words := strings.Split(slug, "-")
	if len(words) >= 2 {
		var b strings.Builder
		for _, w := range words {
			if w != "" {
				b.WriteByte(w[0])
			}
		}
		return b.String()
	}
	if len(slug) > 8 {
		return slug[:8]
	}
	return slug
}

// kickoffRunbook plants a timestamped runbook claim: run or tear down one
// runbook scenario path. The controller launches `factory run` and the
// claim trims when the runner result outdates the click (claims v2).
func (s *Server) kickoffRunbook(c *gin.Context) {
	ctx := c.Request.Context()
	namespace := s.Auth.GetNamespaceFromContext(c)
	sessionUser := s.Auth.GetUserFromContext(c)
	board, _, err := s.resolveBoard(ctx, namespace, sessionUser, c.Param("board"))
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Board not accessible", "details": err.Error()})
		return
	}
	var req struct {
		Mode string `json:"mode"` // plan (default) | deploy | teardown
		// Name is the run's identity. Scenario and Instance are the
		// older spelling of the same thing and are still accepted.
		Name     string `json:"name"`
		Scenario string `json:"scenario"`
		Instance string `json:"instance"`
		// Intent is this run's brief, or the amendment on a re-plan.
		// Guidance is its older name.
		Intent   string `json:"intent"`
		Guidance string `json:"guidance"`
		// Runbook starts a new run from an existing one: a runbook
		// under the repository's .agents/runbooks/, or another run.
		// It is copied in and planned for this run. Plan only.
		Runbook string `json:"runbook"`
		// Target is the pull request the run deploys instead of the
		// default branch. Plan only: the plan pins its head commit.
		Target int `json:"target"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	name := firstNonEmpty(req.Name, req.Instance, req.Scenario)
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	intent := strings.TrimSpace(firstNonEmpty(req.Intent, req.Guidance))
	if req.Mode == "" {
		req.Mode = "run"
	}
	if req.Mode != "run" && req.Mode != "teardown" && req.Mode != "plan" && req.Mode != "deploy" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "mode must be plan, deploy, run, or teardown"})
		return
	}
	runbook := strings.TrimSpace(req.Runbook)
	if runbook != "" {
		switch {
		case req.Mode != "plan":
			c.JSON(http.StatusBadRequest, gin.H{"error": "a runbook starts a plan; it cannot be deployed or torn down directly"})
			return
		case !runbookNameRE.MatchString(runbook):
			c.JSON(http.StatusBadRequest, gin.H{"error": "runbook must be lowercase letters, digits and dashes"})
			return
		case runbook == name:
			c.JSON(http.StatusBadRequest, gin.H{"error": "a run cannot be started from itself; pick a different name"})
			return
		}
	}
	switch {
	case req.Target < 0:
		c.JSON(http.StatusBadRequest, gin.H{"error": "target must be a pull request number"})
		return
	case req.Target > 0 && req.Mode != "plan":
		c.JSON(http.StatusBadRequest, gin.H{"error": "a pull request is pinned by a plan; deploy and teardown execute the pin"})
		return
	case req.Target > 0 && len(name) > maxTargetRunName:
		// The sandbox is named after the run, and it is the repository
		// half that gets cut to fit, never the run's name.
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("run name %q is longer than %d characters", name, maxTargetRunName)})
		return
	}
	// Multi-tenant policy (factory itself only discloses): a member
	// with no configured deploy project must not launch infrastructure
	// runbooks — the pod's metadata default is the PLATFORM cluster's
	// project, never a tenant's deploy target. In-pod runbooks need no
	// project; teardown is allowed so cleanup is never locked out.
	if req.Mode != "teardown" && !strings.HasSuffix(name, "-in-pod") {
		if sec, serr := s.K8sManager.Clientset.CoreV1().Secrets(namespace).Get(ctx, GcpSecretName, v1.GetOptions{}); serr != nil || len(sec.Data["project"]) == 0 {
			c.JSON(http.StatusPreconditionFailed, gin.H{"error": "no GCP project configured — set one in Settings, or name a project in the run guidance and use an -in-pod runbook otherwise"})
			return
		}
	}

	// The intent rides the Request rather than a board annotation. A
	// board-level field is shared by every run and outlives all of
	// them — which is how a question typed for one deployment ended up
	// steering the next. This dies with the click it was typed for.
	filed, err := s.fileRequest(ctx, board, boardv1alpha1.RequestSpec{
		Verb:   boardv1alpha1.VerbRun,
		Member: namespace,
		Run: &boardv1alpha1.RunRequest{
			Mode:     req.Mode,
			Name:     name,
			Scenario: req.Scenario,
			Intent:   clampIntent(intent),
			Runbook:  runbook,
			Target:   req.Target,
		},
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to record runbook request", "details": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "requested", "key": filed.Spec.Key(), "request": filed.Name})
}

// getBoardRunbooks reads the Try tab's world: the runbooks on the fork
// branch (tier line and target paths parsed from each, degrading to a
// single unnamed path when parsing finds none), the newest receipts and
// scripts, the run sandboxes, and standing claims as pending states.
func (s *Server) getBoardRunbooks(c *gin.Context) {
	ctx := c.Request.Context()
	namespace := s.Auth.GetNamespaceFromContext(c)
	sessionUser := s.Auth.GetUserFromContext(c)
	board, member, err := s.resolveBoard(ctx, namespace, sessionUser, c.Param("board"))
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Board not accessible", "details": err.Error()})
		return
	}
	repoURL, _, _ := unstructured.NestedString(board.Object, "spec", "repoURL")
	owner, repo, err := parseRepoURL(repoURL)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Invalid repoURL on board"})
		return
	}

	out := gin.H{"forkOwner": member, "sandboxes": []gin.H{}, "pending": []gin.H{}, "repoShort": repoShortName(repo), "runsBranch": runsBranch}
	// The banner signal: with no deploy project, generation cannot
	// verify feasibility and -gcp plans are BLOCKED by contract.
	if sec, serr := s.K8sManager.Clientset.CoreV1().Secrets(namespace).Get(ctx, GcpSecretName, v1.GetOptions{}); serr == nil {
		out["gcpProject"] = string(sec.Data["project"])
	} else {
		out["gcpProject"] = ""
	}

	instances := []gin.H{}
	token, terr := s.memberToken(ctx, namespace)
	if terr == nil {
		gh := githubClientForToken(ctx, token)
		ref := &github.RepositoryContentGetOptions{Ref: runsBranch}

		// Runs: one directory per run, holding its own runbook.md, the
		// scripts generated from it, and the receipts. The legacy
		// layout is read too — `factory runbook` kept artifacts under
		// runbook-deployments/<instance>/ with the procedure in a
		// shared document — because those deployments are live and
		// their teardown has to stay reachable until each is adopted.
		instances = s.scanRunDirectories(ctx, gh, member, repo, ref)
		sort.Slice(instances, func(i, j int) bool { return instances[i]["name"].(string) < instances[j]["name"].(string) })
		out["instances"] = instances
		// What a new run can start from, besides the runs above.
		out["repoRunbooks"] = repoRunbooks(ctx, gh, owner, repo)
	}

	if list, lerr := s.K8sManager.Client.Resource(k8s.SandboxGVR).Namespace(namespace).List(ctx, v1.ListOptions{
		LabelSelector: "sandbox.gemini.google.com/type=runbook",
	}); lerr == nil {
		boardEngine, _, _ := unstructured.NestedString(board.Object, "spec", "sandbox", "engine")
		sandboxes := []gin.H{}
		for _, sb := range list.Items {
			annotations := sb.GetAnnotations()
			if annotations["repo"] != repo {
				continue
			}
			// A sandbox from an older factory, whose first run nobody
			// stamped, falls back to the board's engine so the icon
			// renders.
			engine := sandboxEngine(annotations, engineOrDefault(boardEngine))
			row := gin.H{
				"name":      sb.GetName(),
				"scenario":  annotations["sandbox.gemini.google.com/runbook-scenario"],
				"instance":  annotations["sandbox.gemini.google.com/runbook-instance"],
				"taskState": annotations[annoTaskState],
				"engine":    engine,
			}
			// Aliveness: the Running annotation goes stale when the
			// watching CLI dies before the in-pod task does — ask the
			// pod for the truth (newest task's pid + exit_code + start).
			if annotations[annoTaskState] == factorycli.TaskStateRunning {
				if podID, perr := sandbox.FindSandboxPodInNamespace(ctx, sb.GetName(), namespace); perr == nil && podID != nil {
					var stdout bytes.Buffer
					script := runLivenessScript()
					if eerr := sandbox.ExecInPod(ctx, s.K8sManager.KubeClient, *podID, sandbox.ExecOptions{
						Command: []string{"sh", "-c", script},
						Stdout:  &stdout,
					}); eerr == nil {
						parts := strings.SplitN(strings.TrimSpace(stdout.String()), "|", 3)
						if len(parts) == 3 {
							alive := parts[0] == "yes"
							row["taskAlive"] = alive
							row["taskExit"] = parts[1]
							if secs, aerr := strconv.ParseInt(parts[2], 10, 64); aerr == nil {
								row["taskStartedAt"] = time.Unix(secs, 0).UTC().Format(time.RFC3339)
							}
							// Self-heal the stale Running annotation: the
							// watcher that would have stamped the final
							// state died with its leash, and one-shot
							// claims mean no relaunch will correct it.
							// Leaving it Running exempts the sandbox from
							// idle-pause and disables its row's actions.
							if !alive && parts[1] != "" {
								state := factorycli.TaskStateCompleted
								if parts[1] != "0" {
									state = factorycli.TaskStateFailed
								}
								row["taskState"] = state
								now := time.Now().UTC().Format(time.RFC3339)
								_ = s.K8sManager.UpdateSandboxAnnotation(ctx, namespace, sb.GetName(), annoTaskState, state)
								_ = s.K8sManager.UpdateSandboxAnnotation(ctx, namespace, sb.GetName(), "sandbox.gemini.google.com/completion-time", now)
								_ = s.K8sManager.UpdateSandboxAnnotation(ctx, namespace, sb.GetName(), "sandbox.gemini.google.com/last-task-time", now)
							}
						}
					}
				}
			}
			sandboxes = append(sandboxes, row)
		}
		out["sandboxes"] = sandboxes

		// A run the fork does not have may be local-only: read it from
		// its sandbox. Only once the fork has been read — without it,
		// every run would look local.
		if terr == nil {
			onFork := map[string]bool{}
			for _, inst := range instances {
				onFork[inst["name"].(string)] = true
			}
			added := false
			for _, sb := range sandboxes {
				run, _ := sb["instance"].(string)
				if run == "" || onFork[run] {
					continue
				}
				if row := s.readLocalRun(ctx, namespace, board.GetName(), sb["name"].(string), repo, run); row != nil {
					instances = append(instances, row)
					added = true
				}
			}
			if added {
				sort.Slice(instances, func(i, j int) bool { return instances[i]["name"].(string) < instances[j]["name"].(string) })
				out["instances"] = instances
			}
		}
	}

	// The run clicks: queued and running ones, and the failures.
	//
	// A failure belongs here for the same reason it outlives a success
	// by a week. A click that never launched used to leave this tab
	// exactly as empty as it was before the click — no receipt on the
	// branch, no row, nothing to read — which is the twenty minutes of
	// staring at nothing that started all this. Successes are dropped:
	// the run itself is the row by then.
	//
	// Newest first, and only the newest of each (mode, run): the claim
	// is taken before the success filter below, so last week's failure
	// cannot come back out from under this morning's success on the
	// same thing. Different modes of one run DO coexist here — a failed
	// deploy and a live re-plan are two facts — and the tab is what
	// decides which of them a row speaks for.
	clicks, err := s.listRequests(ctx, board.GetNamespace(), v1.ListOptions{
		LabelSelector: boardv1alpha1.LabelBoard + "=" + board.GetName() + "," +
			boardv1alpha1.LabelVerb + "=" + boardv1alpha1.VerbRun,
	})
	if err != nil {
		// The runs themselves are the more important half of the answer.
		clicks = nil
	}
	pending := []gin.H{}
	seen := map[string]bool{}
	for _, req := range clicks {
		if req.Spec.Run == nil || seen[req.Spec.Key()] {
			continue
		}
		seen[req.Spec.Key()] = true
		if !req.Active() && req.Status.Phase != boardv1alpha1.RequestFailed {
			continue
		}
		pending = append(pending, gin.H{
			"mode":     req.Spec.Run.Mode,
			"scenario": firstNonEmpty(req.Spec.Run.Scenario, req.Spec.Run.Name),
			"instance": req.Spec.Run.Name,
			"phase":    req.Status.Phase,
			"reason":   req.Status.Reason,
			"message":  req.Status.Message,
		})
	}
	sort.Slice(pending, func(i, j int) bool {
		return fmt.Sprint(pending[i]["instance"], pending[i]["mode"]) < fmt.Sprint(pending[j]["instance"], pending[j]["mode"])
	})
	out["pending"] = pending

	c.JSON(http.StatusOK, out)
}

// runsPath is where `factory run` writes; legacyRunPaths are where a
// run may still be found until it is adopted, which happens the first
// time anything touches it.
//
// The middle path is a rename, not history: "runs/" is a bare
// .gitignore entry in a great many repositories — TensorBoard and
// friends write there — and a bare pattern matches at any depth, so
// docs-exploration/runs/ was ignored wherever that appeared. The
// symptom was quiet: agents could not read files under it, and a new
// run's commit staged nothing at all.
const runsPath = "docs-exploration/agent-runs"

var legacyRunPaths = []string{
	"docs-exploration/runbook-deployments",
	"docs-exploration/runs",
}

// The two branches on the member's fork that agents write to. They are
// deliberately not one branch.
//
// runsBranch carries live deployment state: the scripts that tear a
// cluster down, and the receipts that say whether it is still up. The
// runs read path prunes it — removing a run deletes files from it — and
// it is written only by `factory run`, which no member's conversation
// steers directly.
//
// notesBranch carries prose, is archival, and is written by the
// research path, where the agent may be running with approvals turned
// off. Keeping the two apart means an auto-approving session cannot
// push next to a teardown script, and the pruning that clears runs is
// never pointed at the branch that is supposed to keep everything.
//
// runsBranch must match RUNS_BRANCH in factory's run.sh. Nothing links
// the two, so changing one alone leaves the runs on a branch no one
// reads.
const (
	runsBranch  = "research/runs"
	notesBranch = "research/notes"
)

// deployedVerdicts are the receipt verdicts that settle whether
// infrastructure exists. PLANNED is absent on purpose: planning does
// not deploy anything, and a re-plan of a live run must not make it
// look torn down — that is what decides whether teardown is offered,
// and getting it wrong strands a running cluster.
//
// FAILED counts as deployed. A failed deploy may have left partial
// resources behind, and offering a teardown that finds nothing is
// cheap next to hiding one that was needed.
var deployedVerdicts = map[string]bool{
	"VERIFIED":            true,
	"DEPLOYED-UNVERIFIED": true,
	"FAILED":              true,
	"PARTIAL":             true,
	"TORN-DOWN":           false,
}

// receiptScanLimit bounds how deep we read to settle deployed-ness.
// Each read is an API call; the deciding receipt is nearly always the
// newest or the one behind it, and a run with a long tail of plans is
// exactly the case we do not want to pay for.
const receiptScanLimit = 5

// scanRunDirectories reads both layouts and returns one row per run.
// A run present in both wins from the new path: adoption moves the
// directory, and a stale legacy copy must not shadow it.
func (s *Server) scanRunDirectories(ctx context.Context, gh *github.Client, member, repo string, ref *github.RepositoryContentGetOptions) []gin.H {
	byName := map[string]gin.H{}
	order := []string{}
	for _, base := range append(append([]string{}, legacyRunPaths...), runsPath) {
		_, dir, _, derr := gh.Repositories.GetContents(ctx, member, repo, base, ref)
		if derr != nil {
			continue
		}
		for _, entry := range dir {
			if entry.GetType() != "dir" {
				continue
			}
			row := s.readRunDirectory(ctx, gh, member, repo, entry, ref)
			row["legacy"] = base != runsPath
			if _, seen := byName[entry.GetName()]; !seen {
				order = append(order, entry.GetName())
			}
			byName[entry.GetName()] = row
		}
	}
	out := make([]gin.H, 0, len(order))
	for _, n := range order {
		out = append(out, byName[n])
	}
	return out
}

// readRunDirectory turns one run's directory into a row: its files,
// the newest receipt for display, and whether anything is deployed.
func (s *Server) readRunDirectory(ctx context.Context, gh *github.Client, member, repo string, entry *github.RepositoryContent, ref *github.RepositoryContentGetOptions) gin.H {
	row := gin.H{"name": entry.GetName(), "htmlURL": entry.GetHTMLURL(), "files": []gin.H{}}
	_, sub, _, serr := gh.Repositories.GetContents(ctx, member, repo, entry.GetPath(), ref)
	if serr != nil {
		return row
	}
	files := []gin.H{}
	receipts := []gin.H{}
	for _, f := range sub {
		if f.GetType() != "file" {
			continue
		}
		fh := gin.H{"name": f.GetName(), "path": f.GetPath(), "htmlURL": f.GetHTMLURL()}
		files = append(files, fh)
		switch {
		case strings.HasPrefix(f.GetName(), "receipt-"):
			receipts = append(receipts, fh)
		case f.GetName() == "runbook.md":
			// The review surface. A legacy deployment has none until
			// its first deploy or teardown reconciles one.
			row["runbook"] = fh
		case f.GetName() == runTargetFile:
			// The pull request this run deploys: one read, and only for
			// the runs that have one.
			if pr, sha := s.runTarget(ctx, gh, member, repo, f.GetPath(), ref); pr > 0 {
				row["target"] = pr
				row["targetSHA"] = sha
			}
		}
	}
	row["files"] = files
	// Newest first: receipt names carry a UTC timestamp, so the name
	// sorts the same way the clock does.
	sort.Slice(receipts, func(i, j int) bool {
		return receipts[i]["name"].(string) > receipts[j]["name"].(string)
	})
	applyReceiptVerdicts(row, receipts, func(r gin.H) string {
		return s.receiptVerdict(ctx, gh, member, repo, r["path"].(string), ref)
	})
	return row
}

// applyReceiptVerdicts sets the row's latestReceipt and deployed from
// its receipts, newest first, reading each one's verdict with verdictOf.
func applyReceiptVerdicts(row gin.H, receipts []gin.H, verdictOf func(gin.H) string) {
	if len(receipts) == 0 {
		return
	}

	deployedDecided := false
	for i, r := range receipts {
		if i >= receiptScanLimit {
			break
		}
		verdict := verdictOf(r)
		if verdict == "" {
			continue
		}
		r["verdict"] = verdict
		if i == 0 {
			// The newest receipt is what the row displays, whatever it
			// says — including PLANNED for a re-planned live run.
			row["latestReceipt"] = r
		}
		if deployedDecided {
			continue
		}
		// Presence in the map is what makes a verdict decisive; its
		// value is the answer. PLANNED and BLOCKED are absent, so a
		// re-plan of a live run leaves the earlier deploy deciding.
		if deployed, decisive := deployedVerdicts[verdictWord(verdict)]; decisive {
			row["deployed"] = deployed
			deployedDecided = true
		}
	}
	if _, ok := row["latestReceipt"]; !ok {
		row["latestReceipt"] = receipts[0]
	}
}

// verdictWord takes the first token of a verdict line, so
// "VERIFIED (3 resources)" still reads as VERIFIED.
func verdictWord(verdict string) string {
	word, _, _ := strings.Cut(strings.TrimSpace(verdict), " ")
	return strings.ToUpper(word)
}

// receiptVerdict reads a receipt's first line, which is the verdict by
// contract with the run prompts.
func (s *Server) receiptVerdict(ctx context.Context, gh *github.Client, member, repo, path string, ref *github.RepositoryContentGetOptions) string {
	rf, _, _, rerr := gh.Repositories.GetContents(ctx, member, repo, path, ref)
	if rerr != nil || rf == nil {
		return ""
	}
	content, cerr := rf.GetContent()
	if cerr != nil {
		return ""
	}
	line, _, _ := strings.Cut(strings.TrimSpace(content), "\n")
	return strings.TrimSpace(line)
}

// runTargetFile is where factory pins the pull request a run deploys
// (`factory run plan --target`).
const runTargetFile = "target.env"

// maxTargetRunName keeps a pull request run's sandbox name inside the
// DNS budget RunbookSandboxName works to: the run's name is kept whole.
const maxTargetRunName = 40

// runTarget reads a run's target.env: TARGET_PR=<n> and
// TARGET_SHA=<commit>, one per line. Anything else reads as no target.
func (s *Server) runTarget(ctx context.Context, gh *github.Client, member, repo, path string, ref *github.RepositoryContentGetOptions) (int, string) {
	rf, _, _, err := gh.Repositories.GetContents(ctx, member, repo, path, ref)
	if err != nil || rf == nil {
		return 0, ""
	}
	content, err := rf.GetContent()
	if err != nil {
		return 0, ""
	}
	return parseRunTarget(content)
}

// parseRunTarget reads target.env's content.
func parseRunTarget(content string) (int, string) {
	pr, sha := 0, ""
	for _, line := range strings.Split(content, "\n") {
		k, v, _ := strings.Cut(strings.TrimSpace(line), "=")
		switch k {
		case "TARGET_PR":
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				pr = n
			}
		case "TARGET_SHA":
			sha = v
		}
	}
	return pr, sha
}

// firstNonEmpty returns the first value with content, so the older
// request spellings keep working while the UI moves over.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}

// intentClaimLimit bounds what rides an annotation. Kubernetes caps
// all annotations on an object at 256KB together, and a board carries
// one claim per queued run; a brief longer than this is a document,
// and belongs in the run's own runbook.md.
const intentClaimLimit = 4000

// clampIntent keeps a claim from being the thing that makes a board
// unwritable. Newlines go too: the claim is a single annotation value
// read back by splitting on "|".
func clampIntent(intent string) string {
	intent = strings.ReplaceAll(intent, "|", "/")
	intent = strings.Join(strings.Fields(intent), " ")
	if len(intent) > intentClaimLimit {
		return intent[:intentClaimLimit]
	}
	return intent
}

// runLivenessScript asks a run's sandbox whether its newest run task is
// still alive, printing alive|exit_code|started. It mirrors factory's
// own liveness check: an exit code ends the story, a zombie is not
// alive (kill -0 succeeds on Z), and start_time guards against PID
// reuse.
//
// It globbed runbook-* for as long as factory has written run-*, so it
// found nothing, a stale Running was never healed, and the row stayed
// disabled with its sandbox exempt from idle-pause.
func runLivenessScript() string {
	return `d=$(ls -dt /workspaces/tasks/` + factorycli.RunTaskPrefix + `-* 2>/dev/null | head -1); [ -n "$d" ] || exit 0; ` +
		`code=$(cat $d/exit_code 2>/dev/null); pid=$(cat $d/pid 2>/dev/null); alive=no; ` +
		`if [ -z "$code" ] && [ -n "$pid" ]; then ` +
		`stat=$(ps -o stat= -p "$pid" 2>/dev/null | cut -c1); ` +
		`want=$(cat $d/start_time 2>/dev/null | xargs); got=$(ps -p "$pid" -o lstart= 2>/dev/null | xargs); ` +
		`if kill -0 "$pid" 2>/dev/null && [ -n "$stat" ] && [ "$stat" != "Z" ] && [ -n "$got" ] && { [ -z "$want" ] || [ "$want" = "$got" ]; }; then alive=yes; else code=137; echo 137 > $d/exit_code 2>/dev/null || true; fi; fi; ` +
		`echo "$alive|$code|$(stat -c %Y $d/pid 2>/dev/null)"`
}

// runbookNameRE is the shape factory slugs a runbook name to, and the
// Request CRD's pattern for it.
var runbookNameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// repoRunbooksPath is where a repository keeps runbooks for its runs to
// start from: one directory each, in whatever shape — the plan makes a
// run out of it.
const repoRunbooksPath = ".agents/runbooks"

// repoRunbookCache holds each repository's runbook list. The Runs tab
// polls every ten seconds, and the list changes when a PR merges: a
// directory listing per poll would be most of the tab's GitHub budget
// for an answer that is almost always the same.
var repoRunbookCache = struct {
	sync.Mutex
	entries map[string]repoRunbookEntry
}{entries: map[string]repoRunbookEntry{}}

type repoRunbookEntry struct {
	names   []string
	expires time.Time
}

// repoRunbooks lists the runbooks on the repository's default branch —
// what factory reads — as names, sorted. None is an answer, not an
// error: most repositories have no .agents/runbooks/ at all. A failed
// read keeps whatever was known and retries soon rather than parking an
// empty list for the full TTL.
func repoRunbooks(ctx context.Context, gh *github.Client, owner, repo string) []string {
	key := owner + "/" + repo
	repoRunbookCache.Lock()
	e, ok := repoRunbookCache.entries[key]
	repoRunbookCache.Unlock()
	if ok && time.Now().Before(e.expires) {
		return e.names
	}
	names := []string{}
	ttl := 10 * time.Minute
	_, dir, resp, err := gh.Repositories.GetContents(ctx, owner, repo, repoRunbooksPath, nil)
	switch {
	case err == nil:
		for _, entry := range dir {
			if entry.GetType() == "dir" && runbookNameRE.MatchString(entry.GetName()) {
				names = append(names, entry.GetName())
			}
		}
		sort.Strings(names)
	case resp != nil && resp.StatusCode == http.StatusNotFound:
	default:
		names = e.names
		if names == nil {
			names = []string{}
		}
		ttl = time.Minute
	}
	repoRunbookCache.Lock()
	repoRunbookCache.entries[key] = repoRunbookEntry{names: names, expires: time.Now().Add(ttl)}
	repoRunbookCache.Unlock()
	return names
}
