package api

import (
	"bytes"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/sandbox"
)

// The sandbox card: what actually happened inside a sandbox. Tasks are
// nohup-launched by envd into /workspaces/tasks/<type>-<timestamp>/ with
// pid / start_time / exit_code / execution.log files — that layout IS the
// task history, read live via pod exec (no factory changes, no extra
// state). Paused sandboxes have no pod: the card offers Wake instead.
// Scope: the session member's own namespace only — personal boards keep
// every sandbox a member can act on in their namespace.

type sandboxTask struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	StartedAt string `json:"startedAt,omitempty"`
	Status    string `json:"status"` // running | succeeded | failed | aborted
	ExitCode  string `json:"exitCode,omitempty"`
	LogBytes  int64  `json:"logBytes"`
}

type sandboxCardView struct {
	Name      string        `json:"name"`
	Paused    bool          `json:"paused"`
	Starting  bool          `json:"starting,omitempty"`
	TaskState string        `json:"taskState,omitempty"`
	TaskType  string        `json:"taskType,omitempty"`
	Tasks     []sandboxTask `json:"tasks"`
}

var safeTaskName = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

const listTasksScript = `for d in /workspaces/tasks/*/; do
  [ -d "$d" ] || continue
  n=$(basename "$d")
  ec=$(cat "$d/exit_code" 2>/dev/null)
  run=no
  if [ -z "$ec" ] && [ -f "$d/pid" ] && kill -0 "$(cat "$d/pid" 2>/dev/null)" 2>/dev/null; then run=yes; fi
  sz=$(wc -c < "$d/execution.log" 2>/dev/null || echo 0)
  st=$(cat "$d/start_time" 2>/dev/null | head -1)
  printf '%s|%s|%s|%s|%s\n' "$n" "$ec" "$run" "$sz" "$st"
done`

func (s *Server) getSandboxCard(c *gin.Context) {
	ctx := c.Request.Context()
	namespace := s.Auth.GetNamespaceFromContext(c)
	name := c.Param("name")
	if !safeTaskName.MatchString(name) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid sandbox name"})
		return
	}

	sb, err := s.K8sManager.Client.Resource(k8s.SandboxGVR).Namespace(namespace).Get(ctx, name, v1.GetOptions{})
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "sandbox not found", "details": err.Error()})
		return
	}
	annotations := sb.GetAnnotations()
	view := sandboxCardView{
		Name:      name,
		TaskState: annotations["sandbox.gemini.google.com/last-task-state"],
		TaskType:  annotations["sandbox.gemini.google.com/last-task-type"],
		Tasks:     []sandboxTask{},
	}
	replicas, found, _ := unstructured.NestedInt64(sb.Object, "spec", "replicas")
	if found && replicas == 0 {
		view.Paused = true
		c.JSON(http.StatusOK, view)
		return
	}

	podID, err := sandbox.FindSandboxPodInNamespace(ctx, name, namespace)
	if err != nil || podID == nil {
		view.Starting = true
		c.JSON(http.StatusOK, view)
		return
	}

	var stdout, stderr bytes.Buffer
	if err := sandbox.ExecInPod(ctx, s.K8sManager.KubeClient, *podID, sandbox.ExecOptions{
		Command: []string{"sh", "-c", listTasksScript},
		Stdout:  &stdout,
		Stderr:  &stderr,
	}); err != nil {
		// Pod exists but exec failed (booting, terminating): starting is
		// the honest render, not an error page.
		view.Starting = true
		c.JSON(http.StatusOK, view)
		return
	}

	for _, line := range strings.Split(stdout.String(), "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "|", 5)
		if len(parts) < 4 || parts[0] == "" {
			continue
		}
		task := sandboxTask{Name: parts[0], ExitCode: parts[1]}
		task.Type, task.StartedAt = splitTaskName(parts[0])
		if task.StartedAt == "" && len(parts) > 4 {
			if st := strings.TrimSpace(parts[4]); st != "" {
				task.StartedAt = st
			}
		}
		switch {
		case parts[1] == "0":
			task.Status = "succeeded"
		case parts[1] != "":
			task.Status = "failed"
		case parts[2] == "yes":
			task.Status = "running"
		default:
			task.Status = "aborted"
		}
		if n, err := strconv.ParseInt(parts[3], 10, 64); err == nil {
			task.LogBytes = n
		}
		view.Tasks = append(view.Tasks, task)
	}
	// Names embed <type>-<YYYYMMDD-HHMMSS>: newest first, name as the
	// tiebreak for same-second starts.
	sort.Slice(view.Tasks, func(i, j int) bool {
		ti, tj := taskTimestamp(view.Tasks[i].Name), taskTimestamp(view.Tasks[j].Name)
		if ti != tj {
			return ti > tj
		}
		return view.Tasks[i].Name > view.Tasks[j].Name
	})
	c.JSON(http.StatusOK, view)
}

// splitTaskName turns review-20260918-192435 into ("review",
// "2026-09-18T19:24:35Z"-ish display form).
func splitTaskName(name string) (taskType, startedAt string) {
	ts := taskTimestamp(name)
	if ts == "" {
		return name, ""
	}
	taskType = strings.TrimSuffix(name, "-"+ts)
	if t, err := time.Parse("20060102-150405", ts); err == nil {
		startedAt = t.UTC().Format(time.RFC3339)
	}
	return taskType, startedAt
}

var taskTimestampRE = regexp.MustCompile(`(\d{8}-\d{6})$`)

func taskTimestamp(name string) string {
	m := taskTimestampRE.FindStringSubmatch(name)
	if m == nil {
		return ""
	}
	return m[1]
}

func (s *Server) getSandboxTaskLog(c *gin.Context) {
	ctx := c.Request.Context()
	namespace := s.Auth.GetNamespaceFromContext(c)
	name := c.Param("name")
	task := c.Query("task")
	if !safeTaskName.MatchString(name) || !safeTaskName.MatchString(task) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid name"})
		return
	}
	tail := 32 * 1024
	if v, err := strconv.Atoi(c.Query("tail")); err == nil && v > 0 && v <= 256*1024 {
		tail = v
	}

	podID, err := sandbox.FindSandboxPodInNamespace(ctx, name, namespace)
	if err != nil || podID == nil {
		c.String(http.StatusConflict, "sandbox pod is not running (paused?) — wake it to read logs")
		return
	}
	var stdout, stderr bytes.Buffer
	cmd := fmt.Sprintf("tail -c %d /workspaces/tasks/%s/execution.log 2>/dev/null || echo '(no log yet)'", tail, task)
	if err := sandbox.ExecInPod(ctx, s.K8sManager.KubeClient, *podID, sandbox.ExecOptions{
		Command: []string{"sh", "-c", cmd},
		Stdout:  &stdout,
		Stderr:  &stderr,
	}); err != nil {
		c.String(http.StatusBadGateway, "reading log failed: %v", err)
		return
	}
	c.String(http.StatusOK, stdout.String())
}

func (s *Server) sandboxLifecycle(c *gin.Context) {
	ctx := c.Request.Context()
	namespace := s.Auth.GetNamespaceFromContext(c)
	name := c.Param("name")
	if !safeTaskName.MatchString(name) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid sandbox name"})
		return
	}
	var req struct {
		Action string `json:"action"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "action is required"})
		return
	}
	switch req.Action {
	case "wake":
		// The wake stamp shields the booting pod from pauseFinished
		// (same race the controller-side wakes guard against).
		if err := s.K8sManager.UpdateSandboxAnnotation(ctx, namespace, name, "sandbox.gemini.google.com/unpaused-at", nowRFC3339()); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "wake failed", "details": err.Error()})
			return
		}
		if err := s.K8sManager.ScaleupSandboxByName(ctx, namespace, name); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "wake failed", "details": err.Error()})
			return
		}
	case "pause":
		if err := s.K8sManager.ScaledownSandboxByName(ctx, namespace, name); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "pause failed", "details": err.Error()})
			return
		}
	case "delete":
		// Self-service recovery for wedged sandboxes (half-provisioned
		// Services, corrupted workspaces): destroys the checkout, drafts,
		// and sessions — the UI confirms exactly that. Session-namespace
		// scoping above means members only ever delete their own.
		if err := s.K8sManager.DeleteSandbox(ctx, namespace, name); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "delete failed", "details": err.Error()})
			return
		}
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown action (wake|pause|delete)"})
		return
	}
	c.Status(http.StatusOK)
}
