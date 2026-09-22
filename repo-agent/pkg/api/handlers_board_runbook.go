package api

import (
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
	"github.com/google/go-github/v39/github"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// kickoffRunbook plants a timestamped runbook claim: run or tear down one
// runbook scenario path. The controller launches `factory runbook` and the
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
		Mode     string `json:"mode"` // run (default) | teardown
		Scenario string `json:"scenario"`
		Path     string `json:"path"`
		Instance string `json:"instance"` // default <scenario>[-<path>]
		Guidance string `json:"guidance"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Scenario) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "scenario is required"})
		return
	}
	if req.Mode == "" {
		req.Mode = "run"
	}
	if req.Mode != "run" && req.Mode != "teardown" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "mode must be run or teardown"})
		return
	}

	annotations := board.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	if req.Mode == "run" {
		annotations["board.gemini.google.com/runbook-guidance"] = strings.TrimSpace(req.Guidance)
	}
	requests := map[string]string{}
	if raw := annotations[annoBoardRequests]; raw != "" {
		_ = json.Unmarshal([]byte(raw), &requests)
	}
	key := "runbook-" + req.Mode + "-" + strings.TrimSpace(req.Scenario)
	p, inst := strings.TrimSpace(req.Path), strings.TrimSpace(req.Instance)
	if p != "" || inst != "" {
		key += ":" + p
	}
	if inst != "" {
		key += ":" + inst
	}
	requests[key] = namespace + "|" + nowRFC3339()
	buf, _ := json.Marshal(requests)
	annotations[annoBoardRequests] = string(buf)
	board.SetAnnotations(annotations)
	if _, err := s.K8sManager.Client.Resource(repoBoardGVR).Namespace(board.GetNamespace()).Update(ctx, board, v1.UpdateOptions{}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to record runbook request", "details": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "requested", "key": key})
}

var runbookPathHeading = regexp.MustCompile(`(?m)^###\s+Path\s+—\s+(.+?)\s+\(tier\s+(\d)\)`)
var runbookTierLine = regexp.MustCompile(`(?m)^\*\*Tier\*\*:\s*(.+)$`)

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

	out := gin.H{"forkOwner": member, "runbooks": []gin.H{}, "sandboxes": []gin.H{}, "pending": []gin.H{}}

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
				rb := gin.H{"scenario": scenario, "htmlURL": entry.GetHTMLURL(), "path": entry.GetPath(), "paths": []gin.H{}}
				if file, _, _, ferr := gh.Repositories.GetContents(ctx, member, repo, entry.GetPath(), ref); ferr == nil && file != nil {
					if content, cerr := file.GetContent(); cerr == nil {
						if m := runbookTierLine.FindStringSubmatch(content); m != nil {
							rb["tier"] = strings.TrimSpace(m[1])
						}
						paths := []gin.H{}
						for _, m := range runbookPathHeading.FindAllStringSubmatch(content, -1) {
							paths = append(paths, gin.H{"target": strings.TrimSpace(m[1]), "tier": m[2]})
						}
						rb["paths"] = paths
					}
				}
				runbooks = append(runbooks, rb)
			}
		}
		out["runbooks"] = runbooks

		// Deployment instances: one directory per parameterized
		// deployment (params.env, deploy.sh, teardown.sh, receipts).
		// Content renders through the existing exploration doc endpoint.
		instances := []gin.H{}
		if _, dir, _, derr := gh.Repositories.GetContents(ctx, member, repo, "docs-exploration/runbook-deployments", ref); derr == nil {
			for _, entry := range dir {
				if entry.GetType() != "dir" {
					continue
				}
				inst := gin.H{"name": entry.GetName(), "htmlURL": entry.GetHTMLURL(), "files": []gin.H{}}
				if _, sub, _, serr := gh.Repositories.GetContents(ctx, member, repo, entry.GetPath(), ref); serr == nil {
					files := []gin.H{}
					var newestReceipt gin.H
					for _, f := range sub {
						if f.GetType() != "file" {
							continue
						}
						fh := gin.H{"name": f.GetName(), "path": f.GetPath(), "htmlURL": f.GetHTMLURL()}
						files = append(files, fh)
						if strings.HasPrefix(f.GetName(), "receipt-") {
							if newestReceipt == nil || f.GetName() > newestReceipt["name"].(string) {
								newestReceipt = fh
							}
						}
					}
					inst["files"] = files
					if newestReceipt != nil {
						inst["latestReceipt"] = newestReceipt
					}
				}
				instances = append(instances, inst)
			}
		}
		sort.Slice(instances, func(i, j int) bool { return instances[i]["name"].(string) < instances[j]["name"].(string) })
		out["instances"] = instances
	}

	if list, lerr := s.K8sManager.Client.Resource(k8s.SandboxGVR).Namespace(namespace).List(ctx, v1.ListOptions{
		LabelSelector: "sandbox.gemini.google.com/type=runbook",
	}); lerr == nil {
		sandboxes := []gin.H{}
		for _, sb := range list.Items {
			annotations := sb.GetAnnotations()
			if annotations["repo"] != repo {
				continue
			}
			sandboxes = append(sandboxes, gin.H{
				"name":      sb.GetName(),
				"scenario":  annotations["sandbox.gemini.google.com/runbook-scenario"],
				"path":      annotations["sandbox.gemini.google.com/runbook-path"],
				"instance":  annotations["sandbox.gemini.google.com/runbook-instance"],
				"taskState": annotations[annoTaskState],
				"engine":    annotations["board.gemini.google.com/engine"],
			})
		}
		out["sandboxes"] = sandboxes
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
			segs := strings.SplitN(spec, ":", 3)
			p := gin.H{"mode": mode, "scenario": segs[0], "path": "", "instance": ""}
			if len(segs) > 1 {
				p["path"] = segs[1]
			}
			if len(segs) > 2 {
				p["instance"] = segs[2]
			}
			pending = append(pending, p)
		}
		out["pending"] = pending
	}

	c.JSON(http.StatusOK, out)
}
