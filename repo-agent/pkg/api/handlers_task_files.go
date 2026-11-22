package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/acpd"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
)

// A task's files, for the Files tab of the task-session page: what the task
// left in its directory - its result, step logs, transcript, and whatever
// evidence its recipe kept there (validate-kind's receipts and runs). Read
// live from the sandbox's task server while the sandbox is up, and from the
// archive once it is reaped: factory harvests a task's directory into the
// token-usage collector, which keeps it in GCS (factory/pkg/tokenusage).
// Under /api/overseers/:name, for that overseer's sandboxes and, in the
// archive, its repository's tasks only.

// TaskFileEntry is one file of a task's directory.
type TaskFileEntry struct {
	// Name is the file's path in the task directory: a file of it, or of a
	// directory one down ("steps/01-1-setup-git.log").
	Name string `json:"name"`
	// Size is in bytes; 0 when the source does not say (a live listing).
	Size int64 `json:"size,omitempty"`
}

// TaskFiles is what GET .../task-files/:sandbox/:task answers.
type TaskFiles struct {
	Sandbox string `json:"sandbox"`
	Task    string `json:"task"`
	// Source is where the files were read: "live" (the sandbox's task
	// server) or "archive" (the collector).
	Source string          `json:"source"`
	Files  []TaskFileEntry `json:"files"`
}

// taskFilePart is one element of a task file's path, as factory's task
// server and the collector accept it.
var taskFilePart = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,254}$`)

// validTaskFileName is a file of a task directory or of a directory one
// down - the depth the task server serves and the archive keeps.
func validTaskFileName(name string) bool {
	parts := strings.Split(name, "/")
	if len(parts) > 2 {
		return false
	}
	for _, p := range parts {
		if !taskFilePart.MatchString(p) {
			return false
		}
	}
	return true
}

// maxTaskFileRead bounds one file read: a long transcript runs to a few
// megabytes, as acpd.MaxTranscript says.
const maxTaskFileRead = acpd.MaxTranscript

// errNoTaskFiles is a task neither its sandbox nor the archive has.
var errNoTaskFiles = errors.New("no such task")

// collectorHTTP reads the collector; a test may replace it.
var collectorHTTP = &http.Client{Timeout: 60 * time.Second}

// liveTaskClient is the task server of the sandbox in the URL, when the
// sandbox is there and running in the request's namespace; false, with no
// answer written, otherwise (gone, paused, starting, an image without a
// daemon) - the caller then reads the archive.
func (s *Server) liveTaskClient(c *gin.Context) (*acpd.Client, bool, error) {
	ctx := c.Request.Context()
	namespace := s.sessionNamespace(c)
	sb, err := s.K8sManager.Client.Resource(k8s.SandboxGVR).Namespace(namespace).Get(ctx, c.Param("sandbox"), v1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if replicas, found, _ := unstructured.NestedInt64(sb.Object, "spec", "replicas"); found && replicas == 0 {
		return nil, false, nil
	}
	pod, err := s.researchPod(ctx, namespace, sb.GetName())
	if err != nil || pod == nil {
		return nil, false, err
	}
	client, ok := taskSessionClientForPod(ctx, s.ACPD, pod)
	return client, ok, nil
}

// overseerRepo is the owner/repo of the overseer in the URL: the archive
// holds every overseer's tasks, and an overseer's routes read only its
// repository's.
func (s *Server) overseerRepo(ctx context.Context, name string) (string, error) {
	ov, err := s.K8sManager.GetOverseer(ctx, name)
	if err != nil {
		return "", err
	}
	repoURL, _, _ := unstructured.NestedString(ov.Object, "spec", "repoURL")
	owner, repo, err := parseRepoURL(repoURL)
	if err != nil {
		return "", err
	}
	return owner + "/" + repo, nil
}

// archivedTask is the collector's index entry for the task, read for the
// overseer's repository only.
func (s *Server) archivedTask(ctx context.Context, overseer, sandbox, task string) (*archivedTaskRecord, error) {
	repo, err := s.overseerRepo(ctx, overseer)
	if err != nil {
		return nil, fmt.Errorf("reading overseer %s: %w", overseer, err)
	}
	q := url.Values{"sandbox": {sandbox}, "repo": {repo}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, usageCollectorURL()+"/v1/tasks?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := collectorHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reading the task archive: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("reading the task archive: collector answered %d", resp.StatusCode)
	}
	var index struct {
		Tasks []archivedTaskRecord `json:"tasks"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&index); err != nil {
		return nil, fmt.Errorf("reading the task archive: %w", err)
	}
	for i := range index.Tasks {
		t := &index.Tasks[i]
		if t.Sandbox == sandbox && t.Task == task && strings.EqualFold(t.Repo, repo) {
			return t, nil
		}
	}
	return nil, errNoTaskFiles
}

// archivedTaskRecord is the part of the collector's task record
// (tokenusage.TaskRecord) the Files tab reads.
type archivedTaskRecord struct {
	Repo    string `json:"repo"`
	Sandbox string `json:"sandbox"`
	Task    string `json:"task"`
	Files   []struct {
		Name string `json:"name"`
		Size int64  `json:"size"`
	} `json:"files"`
}

// taskFilesParams are the sandbox and task in the URL, checked; false with a
// 400 written otherwise.
func taskFilesParams(c *gin.Context) (string, string, bool) {
	sandbox, task := c.Param("sandbox"), c.Param("task")
	if !safeSandboxName.MatchString(sandbox) || !safeTaskID.MatchString(task) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid sandbox or task"})
		return "", "", false
	}
	return sandbox, task, true
}

// getTaskFiles lists a task's files: live while its sandbox runs, else from
// the archive.
func (s *Server) getTaskFiles(c *gin.Context) {
	sandbox, task, ok := taskFilesParams(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	out := TaskFiles{Sandbox: sandbox, Task: task, Files: []TaskFileEntry{}}
	client, live, err := s.liveTaskClient(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to look up the sandbox", "details": err.Error()})
		return
	}
	if live {
		names, err := client.TaskFiles(ctx, task)
		switch {
		case err == nil:
			out.Source = "live"
			for _, n := range names {
				if validTaskFileName(n) {
					out.Files = append(out.Files, TaskFileEntry{Name: n})
				}
			}
			sortTaskFiles(out.Files)
			c.JSON(http.StatusOK, out)
			return
		case !errors.Is(err, acpd.ErrNotFound):
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
		// The sandbox runs, but the task's directory is gone: the archive
		// may still have it.
	}
	rec, err := s.archivedTask(ctx, c.Param("name"), sandbox, task)
	switch {
	case errors.Is(err, errNoTaskFiles):
		c.JSON(http.StatusNotFound, gin.H{"error": "no such task in the sandbox or the archive"})
		return
	case err != nil:
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	out.Source = "archive"
	for _, f := range rec.Files {
		if validTaskFileName(f.Name) {
			out.Files = append(out.Files, TaskFileEntry{Name: f.Name, Size: f.Size})
		}
	}
	sortTaskFiles(out.Files)
	c.JSON(http.StatusOK, out)
}

// getTaskFile serves one of a task's files, live or from the archive as
// getTaskFiles lists them. Text only: a file is never served as HTML.
func (s *Server) getTaskFile(c *gin.Context) {
	sandbox, task, ok := taskFilesParams(c)
	if !ok {
		return
	}
	name := strings.TrimPrefix(c.Param("path"), "/")
	if !validTaskFileName(name) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid file name"})
		return
	}
	ctx := c.Request.Context()
	client, live, err := s.liveTaskClient(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to look up the sandbox", "details": err.Error()})
		return
	}
	if live {
		data, err := client.ReadTaskFileUpTo(ctx, task, name, maxTaskFileRead)
		switch {
		case err == nil:
			writeTaskFile(c, name, "live", data)
			return
		case !errors.Is(err, acpd.ErrNotFound):
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
	}
	// The archive, for the overseer's repository's task only.
	if _, err := s.archivedTask(ctx, c.Param("name"), sandbox, task); err != nil {
		if errors.Is(err, errNoTaskFiles) {
			c.JSON(http.StatusNotFound, gin.H{"error": "no such task in the sandbox or the archive"})
			return
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	u := usageCollectorURL() + "/v1/tasks/" + url.PathEscape(sandbox) + "/" + url.PathEscape(task) + "/files/" + escapeTaskFilePath(name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	resp, err := collectorHTTP.Do(req)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "reading the task archive: " + err.Error()})
		return
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		c.JSON(http.StatusNotFound, gin.H{"error": "no file " + name + " in the archived task"})
		return
	default:
		c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("reading the task archive: collector answered %d", resp.StatusCode)})
		return
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxTaskFileRead))
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "reading the task archive: " + err.Error()})
		return
	}
	writeTaskFile(c, name, "archive", data)
}

// escapeTaskFilePath escapes each element of a task file's path.
func escapeTaskFilePath(name string) string {
	parts := strings.Split(name, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// writeTaskFile answers with a task file as text (JSON and Markdown by
// their own types), never as HTML, and never sniffed into it.
func writeTaskFile(c *gin.Context, name, source string, data []byte) {
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("X-Task-File-Source", source)
	c.Header("Content-Disposition", fmt.Sprintf("inline; filename=%q", path.Base(name)))
	c.Data(http.StatusOK, taskFileContentType(name), data)
}

// taskFileContentType is how a task file is served: JSON and Markdown by
// their types, everything else as plain text.
func taskFileContentType(name string) string {
	switch {
	case strings.HasSuffix(name, ".json"), strings.HasSuffix(name, ".ndjson"), strings.HasSuffix(name, ".jsonl"):
		return "application/json; charset=utf-8"
	case strings.HasSuffix(name, ".md"):
		return "text/markdown; charset=utf-8"
	default:
		return "text/plain; charset=utf-8"
	}
}

// sortTaskFiles orders a task's files: the top level first, then each
// directory's, by name.
func sortTaskFiles(files []TaskFileEntry) {
	sort.Slice(files, func(i, j int) bool {
		di, dj := strings.Contains(files[i].Name, "/"), strings.Contains(files[j].Name, "/")
		if di != dj {
			return !di
		}
		return files[i].Name < files[j].Name
	})
}
