package api

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/models"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/klog/v2"
)

func (s *Server) getOverseers(c *gin.Context) {
	log := klog.FromContext(c.Request.Context())
	overseers, err := s.K8sManager.ListOverseers(c.Request.Context())
	if err != nil {
		log.Error(err, "Failed to list overseers")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list overseers"})
		return
	}

	items := make([]map[string]interface{}, len(overseers.Items))
	for i, ov := range overseers.Items {
		items[i] = ov.Object
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
	c.JSON(http.StatusOK, overseer.Object)
}

func (s *Server) getOverseerSandboxes(c *gin.Context) {
	name := c.Param("name")
	namespace := fmt.Sprintf("overseer-%s", name)

	// Get all sandboxes in the namespace
	sandboxes, err := s.K8sManager.ListSandboxes(c.Request.Context(), namespace, "")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list sandboxes"})
		return
	}

	items := make([]map[string]interface{}, 0)
	for _, sb := range sandboxes.Items {
		labels := sb.GetLabels()
		sType := labels["sandbox.gemini.google.com/type"]
		if sType == "" {
			sType = labels["sandbox-type"]
		}
		// Skip agent and chore, since they are handled elsewhere
		if sType == "agent" || sType == "chore" || sType == "overseer" {
			continue
		}
		items = append(items, sb.Object)
	}
	c.JSON(http.StatusOK, items)
}

func (s *Server) getOverseerChores(c *gin.Context) {
	name := c.Param("name")
	// Chores are sandboxes in the overseer namespace with specific labels
	namespace := fmt.Sprintf("overseer-%s", name)
	labelSelector := fmt.Sprintf("review.gemini.google.com/overseer=%s,sandbox.gemini.google.com/type=chore", name)
	sandboxes, err := s.K8sManager.ListSandboxes(c.Request.Context(), namespace, labelSelector)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list chore sandboxes"})
		return
	}

	items := make([]map[string]interface{}, len(sandboxes.Items))
	for i, sb := range sandboxes.Items {
		items[i] = sb.Object
	}
	c.JSON(http.StatusOK, items)
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

func (s *Server) getChoreLogs(c *gin.Context) {
	overseerName := c.Param("name")
	choreSandboxName := c.Param("name")
	taskID := c.Query("taskID")

	namespace := fmt.Sprintf("overseer-%s", overseerName)

	// If taskID is provided, proxy to agentserver.
	// Otherwise, return pod logs.

	if taskID != "" {
		serviceName := fmt.Sprintf("%s-lb", choreSandboxName)
		targetURL := fmt.Sprintf("http://%s.%s.svc.cluster.local:13339", serviceName, namespace)

		proxyURL, err := url.Parse(targetURL)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Invalid target URL"})
			return
		}

		proxy := httputil.NewSingleHostReverseProxy(proxyURL)
		proxy.Director = func(req *http.Request) {
			req.URL.Scheme = proxyURL.Scheme
			req.URL.Host = proxyURL.Host
			req.URL.Path = fmt.Sprintf("/logs/%s", taskID)
		}
		proxy.ServeHTTP(c.Writer, c.Request)
		return
	}

	// Find pod by label
	pods, err := s.K8sManager.Clientset.CoreV1().Pods(namespace).List(c.Request.Context(), v1.ListOptions{
		LabelSelector: fmt.Sprintf("sandbox=%s", choreSandboxName),
	})
	if err != nil || len(pods.Items) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "Chore pod not found"})
		return
	}
	podName := pods.Items[0].Name

	// Fallback to pod logs if no taskID
	req := s.K8sManager.Clientset.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{})
	readCloser, err := req.Stream(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to stream chore pod logs", "details": err.Error()})
		return
	}
	defer readCloser.Close()

	c.Header("Content-Type", "text/plain")
	if _, err := io.Copy(c.Writer, readCloser); err != nil {
		klog.Errorf("failed to copy logs to response: %v", err)
	}
}

func (s *Server) getChoreTasks(c *gin.Context) {
	overseerName := c.Param("name")
	choreSandboxName := c.Param("choreName")
	namespace := fmt.Sprintf("overseer-%s", overseerName)

	tasks, err := s.K8sManager.ListSandboxTasks(c.Request.Context(), namespace, choreSandboxName)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list chore tasks", "details": err.Error()})
		return
	}

	modelsTasks := []models.Task{}
	for _, taskItem := range tasks.Items {
		taskType := taskItem.Spec.Type
		taskState := taskItem.Status.TaskState
		result := taskItem.Status.Result

		tAgentDraft := ""
		tUserDraft := ""
		tAgentState := ""
		tAgentStateMessage := ""

		tAnnotations := taskItem.GetAnnotations()
		if tAnnotations != nil {
			tAgentDraft = tAnnotations["agentDraft"]
			tUserDraft = tAnnotations["userDraft"]
			tAgentState = tAnnotations["agentState"]
			tAgentStateMessage = tAnnotations["agentStateMessage"]
		}

		modelsTasks = append(modelsTasks, models.Task{
			Name:              taskItem.GetName(),
			UID:               string(taskItem.GetUID()),
			Type:              taskType,
			TaskState:         taskState,
			Result:            result,
			CreationTimestamp: taskItem.GetCreationTimestamp().Format(time.RFC3339),
			AgentDraft:        tAgentDraft,
			UserDraft:         tUserDraft,
			AgentState:        tAgentState,
			AgentStateMessage: tAgentStateMessage,
			Stats:             convertStats(taskItem.Status.Stats),
		})
	}

	c.JSON(http.StatusOK, modelsTasks)
}

func (s *Server) getChoreTaskLogs(c *gin.Context) {
	// Re-uses getChoreLogs logic but fits the TaskCard route pattern
	overseerName := c.Param("repo")
	choreSandboxName := c.Param("name")
	taskID := c.Param("taskID")

	namespace := fmt.Sprintf("overseer-%s", overseerName)

	serviceName := fmt.Sprintf("%s-lb", choreSandboxName)
	targetURL := fmt.Sprintf("http://%s.%s.svc.cluster.local:13339", serviceName, namespace)

	proxyURL, err := url.Parse(targetURL)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Invalid target URL"})
		return
	}

	proxy := httputil.NewSingleHostReverseProxy(proxyURL)
	proxy.Director = func(req *http.Request) {
		req.URL.Scheme = proxyURL.Scheme
		req.URL.Host = proxyURL.Host
		req.URL.Path = fmt.Sprintf("/logs/%s", taskID)
	}
	proxy.ServeHTTP(c.Writer, c.Request)
}

func (s *Server) pauseChore(c *gin.Context) {
	overseerName := c.Param("name")
	choreName := c.Param("choreName")
	namespace := fmt.Sprintf("overseer-%s", overseerName)

	overseer, err := s.K8sManager.GetOverseer(c.Request.Context(), overseerName)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get overseer"})
		return
	}

	realChoreName := choreName
	sandbox, err := s.K8sManager.Client.Resource(k8s.SandboxGVR).Namespace(namespace).Get(c.Request.Context(), choreName, v1.GetOptions{})
	if err == nil {
		labelName, found, _ := unstructured.NestedString(sandbox.Object, "metadata", "labels", "chore.gemini.google.com/name")
		if found && labelName != "" {
			realChoreName = labelName
		}
	}

	excludeList, found, err := unstructured.NestedStringSlice(overseer.Object, "spec", "chores", "exclude")
	if err != nil || !found {
		excludeList = []string{}
	}

	exists := false
	for _, e := range excludeList {
		if e == realChoreName {
			exists = true
			break
		}
	}

	if !exists {
		excludeList = append(excludeList, realChoreName)
		if err := unstructured.SetNestedStringSlice(overseer.Object, excludeList, "spec", "chores", "exclude"); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to set exclude list"})
			return
		}
		_, err = s.K8sManager.Client.Resource(k8s.OverseerGVR).Update(c.Request.Context(), overseer, v1.UpdateOptions{})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update overseer"})
			return
		}
	}

	err = s.K8sManager.Client.Resource(k8s.SandboxGVR).Namespace(namespace).Delete(c.Request.Context(), choreName, v1.DeleteOptions{})
	if err != nil {
		klog.Errorf("Failed to delete sandbox %s: %v", choreName, err)
	}

	c.JSON(http.StatusOK, gin.H{"message": "Chore paused"})
}

func (s *Server) resumeChore(c *gin.Context) {
	overseerName := c.Param("name")
	choreName := c.Param("choreName")

	overseer, err := s.K8sManager.GetOverseer(c.Request.Context(), overseerName)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get overseer"})
		return
	}

	excludeList, found, err := unstructured.NestedStringSlice(overseer.Object, "spec", "chores", "exclude")
	if !found || err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "Chore already active"})
		return
	}

	newList := make([]string, 0)
	for _, e := range excludeList {
		if e != choreName {
			newList = append(newList, e)
		}
	}

	if len(newList) != len(excludeList) {
		if len(newList) == 0 {
			unstructured.RemoveNestedField(overseer.Object, "spec", "chores", "exclude")
		} else {
			if err := unstructured.SetNestedStringSlice(overseer.Object, newList, "spec", "chores", "exclude"); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to set exclude list"})
				return
			}
		}
		_, err = s.K8sManager.Client.Resource(k8s.OverseerGVR).Update(c.Request.Context(), overseer, v1.UpdateOptions{})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update overseer"})
			return
		}
	}

	c.JSON(http.StatusOK, gin.H{"message": "Chore resumed"})
}
