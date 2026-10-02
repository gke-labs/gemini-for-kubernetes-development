package api

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/sandbox"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Local-only runs.
//
// factory runs a repository the member cannot fork — an organisation's
// policy keeps forks of its private repositories out of personal
// accounts — local-only: research/runs is committed in the run's
// sandbox and never pushed (run.sh's enterLocalOnly). The fork has
// nothing to list, so the Runs tab reads such a run from the sandbox
// instead, and links its files to getLocalRunFile rather than GitHub.
//
// Only committed files are read, never the worktree: they are what
// deploy and teardown execute, and a phase in progress has not
// committed its half-written receipt yet.

// workspacesRoot is where a sandbox keeps its clone, /workspaces/<repo>.
const workspacesRoot = "/workspaces"

// localRunScript lists one run's files on the sandbox's research/runs
// branch: one "<name>\t<first line>" per file, the first line filled
// in only where the row needs it — a receipt's verdict, and target.env
// (its lines joined with spaces). Prints nothing when there is no such
// run. Arguments: the workspaces root, the repository, the run.
//
// safe.directory: the exec does not necessarily run as the user that
// owns the clone, and git refuses to read a repository it does not own.
const localRunScript = `cd "$1/$2" 2>/dev/null || exit 0
g() { git -c safe.directory='*' "$@"; }
g rev-parse -q --verify "refs/heads/` + runsBranch + `" >/dev/null || exit 0
d="` + runsPath + `/$3"
tab="$(printf '\t')"
g ls-tree "` + runsBranch + `:$d" 2>/dev/null | while IFS="$tab" read -r meta name; do
  case "$meta" in *" blob "*) ;; *) continue ;; esac
  line=""
  case "$name" in
    receipt-*) line="$(g show "` + runsBranch + `:$d/$name" | grep -m1 -v '^[[:space:]]*$' | tr -d '\t\r')" ;;
    ` + runTargetFile + `) line="$(g show "` + runsBranch + `:$d/$name" | tr '\n\t\r' '   ')" ;;
  esac
  printf '%s\t%s\n' "$name" "$line"
done
`

// localRunFileScript prints one file of a run from the sandbox's
// research/runs branch. Arguments: the workspaces root, the
// repository, the run, the file.
const localRunFileScript = `cd "$1/$2" && git -c safe.directory='*' show "` + runsBranch + `:` + runsPath + `/$3/$4"`

// localRunFile is one line of localRunScript's output.
type localRunFile struct {
	name, line string
}

func parseLocalRunFiles(out string) []localRunFile {
	var files []localRunFile
	for _, l := range strings.Split(out, "\n") {
		name, line, _ := strings.Cut(l, "\t")
		if name = strings.TrimSpace(name); name == "" {
			continue
		}
		files = append(files, localRunFile{name: name, line: strings.TrimSpace(line)})
	}
	return files
}

// localRunFileURL is where the Runs tab opens a local-only run's file.
func localRunFileURL(board, run, file string) string {
	return fmt.Sprintf("/api/board/%s/runbook/instance/%s/file/%s", url.PathEscape(board), url.PathEscape(run), url.PathEscape(file))
}

// localRunRow is the row readRunDirectory would make of the same files
// on the fork, plus localOnly and the sandbox that holds them. Nil when
// the run has no files.
func localRunRow(board, run, sandboxName string, files []localRunFile) gin.H {
	if len(files) == 0 {
		return nil
	}
	row := gin.H{"name": run, "localOnly": true, "sandbox": sandboxName, "files": []gin.H{}}
	out := []gin.H{}
	receipts := []gin.H{}
	verdicts := map[string]string{}
	for _, f := range files {
		fh := gin.H{"name": f.name, "path": runsPath + "/" + run + "/" + f.name, "htmlURL": localRunFileURL(board, run, f.name)}
		out = append(out, fh)
		switch {
		case strings.HasPrefix(f.name, "receipt-"):
			receipts = append(receipts, fh)
			verdicts[f.name] = f.line
		case f.name == "runbook.md":
			row["runbook"] = fh
		case f.name == runTargetFile:
			if pr, sha := parseRunTarget(strings.ReplaceAll(f.line, " ", "\n")); pr > 0 {
				row["target"] = pr
				row["targetSHA"] = sha
			}
		}
	}
	row["files"] = out
	// There is no directory page to link to; the procedure is the
	// review surface, and the newest receipt stands in without one.
	sort.Slice(receipts, func(i, j int) bool {
		return receipts[i]["name"].(string) > receipts[j]["name"].(string)
	})
	switch {
	case row["runbook"] != nil:
		row["htmlURL"] = row["runbook"].(gin.H)["htmlURL"]
	case len(receipts) > 0:
		row["htmlURL"] = receipts[0]["htmlURL"]
	default:
		row["htmlURL"] = out[0]["htmlURL"]
	}
	applyReceiptVerdicts(row, receipts, func(r gin.H) string { return verdicts[r["name"].(string)] })
	return row
}

// readLocalRun reads a run from its sandbox. Nil when the sandbox has
// no pod — a paused sandbox cannot be read until it wakes — or no
// local record of the run.
func (s *Server) readLocalRun(ctx context.Context, namespace, board, sandboxName, repo, run string) gin.H {
	podID, err := sandbox.FindSandboxPodInNamespace(ctx, sandboxName, namespace)
	if err != nil || podID == nil {
		return nil
	}
	var stdout bytes.Buffer
	if err := sandbox.ExecInPod(ctx, s.K8sManager.KubeClient, *podID, sandbox.ExecOptions{
		Command: []string{"sh", "-c", localRunScript, "sh", workspacesRoot, repo, run},
		Stdout:  &stdout,
	}); err != nil {
		return nil
	}
	return localRunRow(board, run, sandboxName, parseLocalRunFiles(stdout.String()))
}

// runSandboxFor finds the sandbox a run lives in.
func (s *Server) runSandboxFor(ctx context.Context, namespace, repo, run string) (string, error) {
	list, err := s.K8sManager.Client.Resource(k8s.SandboxGVR).Namespace(namespace).List(ctx, v1.ListOptions{
		LabelSelector: "sandbox.gemini.google.com/type=runbook",
	})
	if err != nil {
		return "", err
	}
	for _, sb := range list.Items {
		a := sb.GetAnnotations()
		if a["repo"] == repo && a["sandbox.gemini.google.com/runbook-instance"] == run {
			return sb.GetName(), nil
		}
	}
	return "", nil
}

// getLocalRunFile serves one file of a local-only run, as text, from
// the run's sandbox: the local-only counterpart of a file's GitHub page.
func (s *Server) getLocalRunFile(c *gin.Context) {
	ctx := c.Request.Context()
	namespace := s.Auth.GetNamespaceFromContext(c)
	sessionUser := s.Auth.GetUserFromContext(c)
	board, _, err := s.resolveBoard(ctx, namespace, sessionUser, c.Param("board"))
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
	run, file := c.Param("instance"), c.Param("file")
	// Both end up in a git path: one name each, never a way out of the
	// run's directory.
	if !runbookNameRE.MatchString(run) || file == "" || strings.ContainsAny(file, "/\\") || strings.HasPrefix(file, ".") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid run or file name"})
		return
	}
	sandboxName, err := s.runSandboxFor(ctx, namespace, repo, run)
	if err != nil || sandboxName == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "no sandbox holds run " + run})
		return
	}
	podID, err := sandbox.FindSandboxPodInNamespace(ctx, sandboxName, namespace)
	if err != nil || podID == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "the sandbox holding run " + run + " is not running; wake it to read its files"})
		return
	}
	var stdout, stderr bytes.Buffer
	if err := sandbox.ExecInPod(ctx, s.K8sManager.KubeClient, *podID, sandbox.ExecOptions{
		Command: []string{"sh", "-c", localRunFileScript, "sh", workspacesRoot, repo, run, file},
		Stdout:  &stdout,
		Stderr:  &stderr,
	}); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": file + " not found in run " + run, "details": strings.TrimSpace(stderr.String())})
		return
	}
	c.Data(http.StatusOK, "text/plain; charset=utf-8", stdout.Bytes())
}
