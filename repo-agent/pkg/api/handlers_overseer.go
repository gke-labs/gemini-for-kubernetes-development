package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

func enrichOverseerObject(ov map[string]interface{}) {
	metadata, ok := ov["metadata"].(map[string]interface{})
	if !ok {
		return
	}
	annotations, ok := metadata["annotations"].(map[string]interface{})
	if !ok {
		return
	}
	if val, found := annotations["overseer.gemini.google.com/upgrade-mode"]; found && val == "true" {
		ov["upgradeMode"] = true
		if ts, ok := annotations["overseer.gemini.google.com/upgrade-timestamp"]; ok {
			ov["upgradeTimestamp"] = ts
		}
	}
}

func (s *Server) getOverseers(c *gin.Context) {
	log := klog.FromContext(c.Request.Context())
	overseers, err := s.K8sManager.ListOverseers(c.Request.Context())
	if err != nil {
		log.Error(err, "Failed to list overseers")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list overseers"})
		return
	}

	// Only the overseers the member may see (overseerAllows).
	items := []map[string]interface{}{}
	for i := range overseers.Items {
		ov := &overseers.Items[i]
		if !s.overseerAllows(c, ov) {
			continue
		}
		enrichOverseerObject(ov.Object)
		items = append(items, ov.Object)
	}
	c.JSON(http.StatusOK, items)
}

func (s *Server) getOverseer(c *gin.Context) {
	name := c.Param("name")
	overseer, err := s.K8sManager.GetOverseer(c.Request.Context(), name)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get overseer"})
		return
	}
	enrichOverseerObject(overseer.Object)
	c.JSON(http.StatusOK, overseer.Object)
}

func (s *Server) getOverseerLogs(c *gin.Context) {
	name := c.Param("name")
	namespace := fmt.Sprintf("overseer-%s", name)

	// Find pod by label
	pods, err := s.K8sManager.Clientset.CoreV1().Pods(namespace).List(c.Request.Context(), v1.ListOptions{
		LabelSelector: fmt.Sprintf("sandbox=overseer-%s", name),
	})
	if err != nil || len(pods.Items) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "Overseer pod not found"})
		return
	}
	podName := pods.Items[0].Name

	// Get pod logs
	req := s.K8sManager.Clientset.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{
		Follow: false,
	})
	readCloser, err := req.Stream(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to stream pod logs", "details": err.Error()})
		return
	}
	defer readCloser.Close()

	c.Header("Content-Type", "text/plain")
	_, err = io.Copy(c.Writer, readCloser)
	if err != nil {
		klog.Errorf("failed to copy logs to response: %v", err)
	}
}

type QueueTaskItem struct {
	FileName   string `json:"fileName"`
	QueueState string `json:"queueState"`
	Type       string `json:"type"`
	// Rule and Recipe are the rule that decided the task and the recipe it
	// runs; an older watch serves neither (queueRecipes).
	Rule             string  `json:"rule,omitempty"`
	Recipe           string  `json:"recipe,omitempty"`
	URL              string  `json:"url"`
	Number           int     `json:"number"`
	Priority         string  `json:"priority"`
	Phase            int     `json:"phase"`
	CreatedAt        string  `json:"createdAt"`
	EnqueuedAt       string  `json:"enqueuedAt,omitempty"`
	StartedAt        string  `json:"startedAt,omitempty"`
	CompletedAt      string  `json:"completedAt,omitempty"`
	DurationSeconds  float64 `json:"durationSeconds,omitempty"`
	TriggerEventTime string  `json:"triggerEventTime,omitempty"`
	TriggerReason    string  `json:"triggerReason,omitempty"`
	TriggerNotes     string  `json:"triggerNotes,omitempty"`
	Assignee         string  `json:"assignee"`
	Status           string  `json:"status"`
	CommitSHA        string  `json:"commitSHA"`
	Rank             int     `json:"rank,omitempty"`
}

type QueueResponse struct {
	Summary struct {
		TotalPending    int            `json:"totalPending"`
		TotalProcessing int            `json:"totalProcessing"`
		TotalCompleted  int            `json:"totalCompleted"`
		ByPriority      map[string]int `json:"byPriority"`
		ByType          map[string]int `json:"byType"`
	} `json:"summary"`
	Incoming   []QueueTaskItem `json:"incoming"`
	Processing []QueueTaskItem `json:"processing"`
	Processed  []QueueTaskItem `json:"processed"`
	// Cooldowns are the tasks the watch's issue scanner holds back because
	// the same task finished too recently. An older watch serves none.
	Cooldowns []QueueCooldownItem `json:"cooldowns,omitempty"`
	// Failures are the tasks that failed and have not succeeded since,
	// with what their child said went wrong. An older watch serves none.
	Failures []QueueFailureItem `json:"failures,omitempty"`
	// Rules is the watch rules' summary: each rule's runs, give-ups and
	// tasks dropped at start, and how many recorded decisions the engine
	// decides the same on again; an older watch serves none.
	Rules *QueueRules `json:"rules,omitempty"`
}

// QueueRules is the watch rules' summary, as the watch serves it
// (factory's rules.SummaryView): since the watch started, the decisions
// recorded and checked, how many the engine decides the same on again
// (consistent), how many a gate outside the rules held back, each rule's
// tally, and the latest inconsistencies.
type QueueRules struct {
	Since      string                    `json:"since"`
	Records    int                       `json:"records"`
	Consistent int                       `json:"consistent"`
	Gated      int                       `json:"gated"`
	ByRule     map[string]QueueRuleTally `json:"byRule"`
	Recent     []QueueRulesInconsistency `json:"recent,omitempty"`
}

// QueueRuleTally is one rule's runs and give-ups, and its tasks dropped
// before they started, with why the last one was.
type QueueRuleTally struct {
	Run      int    `json:"run,omitempty"`
	GiveUp   int    `json:"giveUp,omitempty"`
	Dropped  int    `json:"dropped,omitempty"`
	LastDrop string `json:"lastDrop,omitempty"`
}

// QueueRulesInconsistency is a record on which the engine, deciding
// again, decides otherwise than the scanner did: each side's, as
// "<rule> <outcome>".
type QueueRulesInconsistency struct {
	At      string   `json:"at"`
	Kind    string   `json:"kind"`
	Number  int      `json:"number"`
	Rules   []string `json:"rules,omitempty"`
	Scanner []string `json:"scanner,omitempty"`
}

// QueueFailureItem is a task that failed, as the watch serves it.
type QueueFailureItem struct {
	FileName string `json:"fileName"`
	Number   int    `json:"number"`
	URL      string `json:"url,omitempty"`
	Type     string `json:"type"`
	Rule     string `json:"rule,omitempty"`
	Recipe   string `json:"recipe,omitempty"`
	// Error is the child's exit status and the error it printed.
	Error string `json:"error"`
	At    string `json:"at"`
}

// QueueCooldownItem is a task held back by its cooldown, as the watch
// serves it.
type QueueCooldownItem struct {
	FileName        string `json:"fileName"`
	Number          int    `json:"number"`
	URL             string `json:"url,omitempty"`
	Workflow        string `json:"workflow,omitempty"`
	Until           string `json:"until"`
	LastStatus      string `json:"lastStatus,omitempty"`
	LastCompletedAt string `json:"lastCompletedAt,omitempty"`
}

func (s *Server) getOverseerQueue(c *gin.Context) {
	overseerName := c.Param("name")
	namespace := fmt.Sprintf("overseer-%s", overseerName)

	pods, err := s.K8sManager.Clientset.CoreV1().Pods(namespace).List(c.Request.Context(), v1.ListOptions{})
	if err != nil || len(pods.Items) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "Overseer controller pod not found"})
		return
	}

	var targetPod *corev1.Pod
	expectedName := fmt.Sprintf("overseer-%s", overseerName)
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.Name == expectedName || strings.HasPrefix(p.Name, "overseer-") {
			targetPod = p
			break
		}
	}
	if targetPod == nil {
		targetPod = &pods.Items[0]
	}

	podIP := targetPod.Status.PodIP
	if podIP == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Overseer pod has no IP address allocated yet"})
		return
	}

	podURL := fmt.Sprintf("http://%s:13338/api/v1/queue", podIP)
	client := http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(podURL)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"status":     "syncing",
			"isSyncing":  true,
			"message":    "Overseer daemon is currently in cycle sync / Gemini orchestration phase.",
			"summary":    gin.H{"totalPending": 0, "totalProcessing": 0, "totalCompleted": 0},
			"incoming":   []QueueTaskItem{},
			"processing": []QueueTaskItem{},
			"processed":  []QueueTaskItem{},
		})
		return
	}
	defer resp.Body.Close()

	c.Status(resp.StatusCode)
	c.Header("Content-Type", "application/json")
	_, _ = io.Copy(c.Writer, resp.Body)
}

func (s *Server) updateOverseerQueueTaskPriority(c *gin.Context) {
	overseerName := c.Param("name")
	filename := c.Param("filename")
	namespace := fmt.Sprintf("overseer-%s", overseerName)

	var reqBody struct {
		Priority string `json:"priority" binding:"required"`
	}
	if err := c.ShouldBindJSON(&reqBody); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body, priority is required"})
		return
	}

	pods, err := s.K8sManager.Clientset.CoreV1().Pods(namespace).List(c.Request.Context(), v1.ListOptions{})
	if err != nil || len(pods.Items) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "Overseer controller pod not found"})
		return
	}

	var targetPod *corev1.Pod
	expectedName := fmt.Sprintf("overseer-%s", overseerName)
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.Name == expectedName || strings.HasPrefix(p.Name, "overseer-") {
			targetPod = p
			break
		}
	}
	if targetPod == nil {
		targetPod = &pods.Items[0]
	}

	podIP := targetPod.Status.PodIP
	if podIP == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Overseer pod has no IP address allocated yet"})
		return
	}

	cleanFilename := filepath.Base(filename)
	podURL := fmt.Sprintf("http://%s:13338/api/v1/queue/%s/priority", podIP, cleanFilename)
	jsonBytes, _ := json.Marshal(reqBody)
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Post(podURL, "application/json", bytes.NewReader(jsonBytes))
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "Failed to update task priority via Overseer Queue REST service", "details": err.Error()})
		return
	}
	defer resp.Body.Close()

	c.Status(resp.StatusCode)
	c.Header("Content-Type", "application/json")
	_, _ = io.Copy(c.Writer, resp.Body)
}

func (s *Server) getOverseerStatus(c *gin.Context) {
	overseerName := c.Param("name")
	namespace := fmt.Sprintf("overseer-%s", overseerName)

	pods, err := s.K8sManager.Clientset.CoreV1().Pods(namespace).List(c.Request.Context(), v1.ListOptions{
		LabelSelector: fmt.Sprintf("sandbox=overseer-%s", overseerName),
	})
	if err != nil || len(pods.Items) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "Overseer controller pod not found"})
		return
	}

	targetPod := &pods.Items[0]
	podIP := targetPod.Status.PodIP
	if podIP == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Overseer pod has no IP address allocated yet"})
		return
	}

	podURL := fmt.Sprintf("http://%s:13338/api/v1/status", podIP)
	client := http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, podURL, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create status request", "details": err.Error()})
		return
	}
	resp, err := client.Do(req)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"status":            "syncing",
			"isSyncing":         true,
			"message":           "Overseer daemon is currently in cycle sync / Gemini orchestration phase.",
			"total":             0,
			"active":            0,
			"quotaExceeded":     0,
			"suspended":         0,
			"activeList":        []string{},
			"quotaExceededList": []string{},
			"suspendedList":     []string{},
		})
		return
	}
	defer resp.Body.Close()

	c.Status(resp.StatusCode)
	c.Header("Content-Type", "application/json")
	_, _ = io.Copy(c.Writer, resp.Body)
}
