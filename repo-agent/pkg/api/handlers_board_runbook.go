package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
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
	ref := "exploration/notes"
	dir := "docs-exploration/runbook-deployments/" + instance
	_, entries, _, derr := gh.Repositories.GetContents(ctx, member, repo, dir, &github.RepositoryContentGetOptions{Ref: ref})
	if derr != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "instance records not found"})
		return
	}
	msg := "remove records for retired instance " + instance
	for _, e := range entries {
		if e.GetType() != "file" {
			continue
		}
		sha := e.GetSHA()
		if _, _, ferr := gh.Repositories.DeleteFile(ctx, member, repo, e.GetPath(), &github.RepositoryContentFileOptions{
			Message: &msg, SHA: &sha, Branch: &ref,
		}); ferr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed deleting " + e.GetName(), "details": ferr.Error()})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"status": "removed", "instance": instance})
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

	annotations := board.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	requests := map[string]string{}
	if raw := annotations[annoBoardRequests]; raw != "" {
		_ = json.Unmarshal([]byte(raw), &requests)
	}
	key := "runbook-" + req.Mode + "-" + name
	// The intent rides the claim rather than a board annotation. A
	// board-level field is shared by every run and outlives all of
	// them — which is how a question typed for one deployment ended up
	// steering the next. This dies when the claim is consumed.
	requests[key] = namespace + "|" + nowRFC3339() + "|" + clampIntent(intent)
	buf, _ := json.Marshal(requests)
	annotations[annoBoardRequests] = string(buf)
	board.SetAnnotations(annotations)
	if _, err := s.K8sManager.Client.Resource(repoBoardGVR).Namespace(board.GetNamespace()).Update(ctx, board, v1.UpdateOptions{}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to record runbook request", "details": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "requested", "key": key})
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
	_, repo, err := parseRepoURL(repoURL)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Invalid repoURL on board"})
		return
	}

	out := gin.H{"forkOwner": member, "runbooks": []gin.H{}, "sandboxes": []gin.H{}, "pending": []gin.H{}, "repoShort": repoShortName(repo)}
	// The banner signal: with no deploy project, generation cannot
	// verify feasibility and -gcp plans are BLOCKED by contract.
	if sec, serr := s.K8sManager.Clientset.CoreV1().Secrets(namespace).Get(ctx, GcpSecretName, v1.GetOptions{}); serr == nil {
		out["gcpProject"] = string(sec.Data["project"])
	} else {
		out["gcpProject"] = ""
	}

	token, terr := s.memberToken(ctx, namespace)
	if terr == nil {
		gh := githubClientForToken(ctx, token)
		ref := &github.RepositoryContentGetOptions{Ref: "exploration/notes"}

		runbooks := []gin.H{}
		if _, dir, _, derr := gh.Repositories.GetContents(ctx, member, repo, "docs-exploration/runbooks", ref); derr == nil {
			for _, entry := range dir {
				if entry.GetType() != "file" || !strings.HasSuffix(entry.GetName(), ".md") {
					continue
				}
				scenario := strings.TrimSuffix(entry.GetName(), ".md")
				runbooks = append(runbooks, gin.H{"scenario": scenario, "htmlURL": entry.GetHTMLURL(), "path": entry.GetPath()})
			}
		}
		out["runbooks"] = runbooks

		// Runs: one directory per run, holding its own runbook.md, the
		// scripts generated from it, and the receipts. The legacy
		// layout is read too — `factory runbook` kept artifacts under
		// runbook-deployments/<instance>/ with the procedure in a
		// shared document — because those deployments are live and
		// their teardown has to stay reachable until each is adopted.
		instances := s.scanRunDirectories(ctx, gh, member, repo, ref)
		sort.Slice(instances, func(i, j int) bool { return instances[i]["name"].(string) < instances[j]["name"].(string) })
		out["instances"] = instances
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
			// First-run sandboxes carry no engine annotation (it is only
			// stamped on relaunches into an existing sandbox) — fall back
			// to the board's engine so the icon renders.
			engine := annotations["board.gemini.google.com/engine"]
			if engine == "" {
				engine = engineOrDefault(boardEngine)
			}
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
					// Mirrors factory's own liveness check: an exit code
					// ends the story, a zombie is not alive (kill -0
					// succeeds on Z), and start_time guards against PID
					// reuse.
					script := `d=$(ls -dt /workspaces/tasks/runbook-* 2>/dev/null | head -1); [ -n "$d" ] || exit 0; ` +
						`code=$(cat $d/exit_code 2>/dev/null); pid=$(cat $d/pid 2>/dev/null); alive=no; ` +
						`if [ -z "$code" ] && [ -n "$pid" ]; then ` +
						`stat=$(ps -o stat= -p "$pid" 2>/dev/null | cut -c1); ` +
						`want=$(cat $d/start_time 2>/dev/null | xargs); got=$(ps -p "$pid" -o lstart= 2>/dev/null | xargs); ` +
						`if kill -0 "$pid" 2>/dev/null && [ "$stat" != "Z" ] && { [ -z "$want" ] || [ "$want" = "$got" ]; }; then alive=yes; fi; fi; ` +
						`echo "$alive|$code|$(stat -c %Y $d/pid 2>/dev/null)"`
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
	}

	// A standing explore-runbook claim means a draft is being written
	// (the Explore pipeline generates runbooks); the Try tab shows it
	// so a custom runbook's birth is visible where it was requested.
	if raw := board.GetAnnotations()[annoBoardRequests]; raw != "" {
		requests := map[string]string{}
		_ = json.Unmarshal([]byte(raw), &requests)
		if _, ok := requests["explore-runbook"]; ok {
			out["draftPending"] = board.GetAnnotations()["board.gemini.google.com/explore-scenario"]
		}
	}

	// Standing runbook claims are the queued/running states; ones already
	// served (a completion newer than the click) are the trim's business.
	if raw := board.GetAnnotations()[annoBoardRequests]; raw != "" {
		requests := map[string]string{}
		_ = json.Unmarshal([]byte(raw), &requests)
		keys := make([]string, 0, len(requests))
		for key := range requests {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		pending := []gin.H{}
		for _, key := range keys {
			rest, ok := strings.CutPrefix(key, "runbook-")
			if !ok {
				continue
			}
			mode, spec, modeOK := strings.Cut(rest, "-")
			if !modeOK {
				continue
			}
			scenario, inst, _ := strings.Cut(spec, ":")
			pending = append(pending, gin.H{"mode": mode, "scenario": scenario, "instance": inst})
		}
		out["pending"] = pending
	}

	c.JSON(http.StatusOK, out)
}

// runsPath and legacyRunsPath are the two layouts a deployment can
// live in. `factory run` writes the first; `factory runbook` wrote the
// second and is being retired. A run is adopted into the new path the
// first time anything touches it, so both are read until the last
// legacy deployment has been torn down or adopted.
const (
	runsPath       = "docs-exploration/runs"
	legacyRunsPath = "docs-exploration/runbook-deployments"
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
	for _, base := range []string{legacyRunsPath, runsPath} {
		_, dir, _, derr := gh.Repositories.GetContents(ctx, member, repo, base, ref)
		if derr != nil {
			continue
		}
		for _, entry := range dir {
			if entry.GetType() != "dir" {
				continue
			}
			row := s.readRunDirectory(ctx, gh, member, repo, entry, ref)
			row["legacy"] = base == legacyRunsPath
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
		}
	}
	row["files"] = files
	// Newest first: receipt names carry a UTC timestamp, so the name
	// sorts the same way the clock does.
	sort.Slice(receipts, func(i, j int) bool {
		return receipts[i]["name"].(string) > receipts[j]["name"].(string)
	})
	if len(receipts) == 0 {
		return row
	}

	deployedDecided := false
	for i, r := range receipts {
		if i >= receiptScanLimit {
			break
		}
		verdict := s.receiptVerdict(ctx, gh, member, repo, r["path"].(string), ref)
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
	return row
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
