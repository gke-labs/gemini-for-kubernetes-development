package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/sandbox"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
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

	items := make([]map[string]interface{}, len(overseers.Items))
	for i, ov := range overseers.Items {
		enrichOverseerObject(ov.Object)
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
	enrichOverseerObject(overseer.Object)
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
		// Skip the overseer controller sandbox if labeled type=overseer or named equal to the namespace (e.g. overseer-kcc)
		if sType == "overseer" || sb.GetName() == namespace {
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
	c.JSON(http.StatusOK, tasks.Items)
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

func (s *Server) getChoreTaskTelemetry(c *gin.Context) {
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
		req.URL.Path = fmt.Sprintf("/telemetry/%s", taskID)
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{}"))
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

func (s *Server) getOverseerSandboxTasks(c *gin.Context) {
	overseerName := c.Param("name")
	sandboxName := c.Param("sandboxName")
	namespace := fmt.Sprintf("overseer-%s", overseerName)

	// 1. Try checking if there is a SandboxTask CRD in K8s (for backwards compat)
	tasks, err := s.K8sManager.ListSandboxTasks(c.Request.Context(), namespace, sandboxName)
	if err == nil && len(tasks.Items) > 0 {
		c.JSON(http.StatusOK, tasks.Items)
		return
	}

	// 2. Try AgentServer HTTP /tasks on port 13339 if available
	serviceName := fmt.Sprintf("%s-lb", sandboxName)
	targetURL := fmt.Sprintf("http://%s.%s.svc.cluster.local:13339/tasks", serviceName, namespace)
	client := http.Client{Timeout: 2 * time.Second}
	if resp, err := client.Get(targetURL); err == nil && resp.StatusCode == http.StatusOK {
		defer resp.Body.Close()
		var httpTasks []interface{}
		if json.NewDecoder(resp.Body).Decode(&httpTasks) == nil && len(httpTasks) > 0 {
			c.JSON(http.StatusOK, httpTasks)
			return
		}
	}

	// 3. Find pod to exec into /workspaces/tasks
	podID, err := sandbox.FindSandboxPodInNamespace(c.Request.Context(), sandboxName, namespace)
	if err != nil || podID == nil {
		// Fallback to checking the Sandbox CR annotations for last-task-type / last-task-state
		sb, getErr := s.K8sManager.Client.Resource(k8s.SandboxGVR).Namespace(namespace).Get(c.Request.Context(), sandboxName, v1.GetOptions{})
		if getErr == nil {
			ann := sb.GetAnnotations()
			if ann != nil && ann["sandbox.gemini.google.com/last-task-type"] != "" {
				tType := ann["sandbox.gemini.google.com/last-task-type"]
				tState := ann["sandbox.gemini.google.com/last-task-state"]
				if tState == "" {
					tState = "Completed"
				}
				if strings.EqualFold(tState, "Running") {
					conditions, _, _ := unstructured.NestedSlice(sb.Object, "status", "conditions")
					for _, c := range conditions {
						if condMap, ok := c.(map[string]interface{}); ok {
							msg, _ := condMap["message"].(string)
							reason, _ := condMap["reason"].(string)
							if strings.Contains(strings.ToLower(msg), "evicted") || strings.EqualFold(reason, "evicted") {
								tState = "Failed (Evicted)"
								break
							} else if strings.Contains(strings.ToLower(msg), "phase: failed") || strings.EqualFold(reason, "podfailed") {
								tState = "Failed (PodFailed)"
							}
						}
					}
				}
				synth := []map[string]interface{}{
					{
						"metadata": map[string]interface{}{
							"name":      tType,
							"namespace": namespace,
						},
						"spec": map[string]interface{}{
							"taskType": tType,
						},
						"status": map[string]interface{}{
							"state": tState,
						},
					},
				}
				c.JSON(http.StatusOK, synth)
				return
			}
		}
		c.JSON(http.StatusOK, []interface{}{})
		return
	}

	// 4. Exec sh check inside the pod via Stdin
	var stdout bytes.Buffer
	peekScript := fmt.Sprintf(`if command -v python3 >/dev/null 2>&1; then
  python3 -c '
import os, json
tasks_dir = "/workspaces/tasks"
res = []
if os.path.exists(tasks_dir):
    for d in sorted(os.listdir(tasks_dir), reverse=True):
        p = os.path.join(tasks_dir, d)
        if not os.path.isdir(p): continue
        status = "Pending"
        ec = None
        if os.path.exists(os.path.join(p, "exit_code")):
            try:
                with open(os.path.join(p, "exit_code")) as f: ec = f.read().strip()
                status = "Completed" if ec == "0" else "Failed"
            except: pass
        elif os.path.exists(os.path.join(p, "pid")):
            try:
                with open(os.path.join(p, "pid")) as f: pid = int(f.read().strip())
                try:
                    os.kill(pid, 0)
                    status = "Running"
                except OSError: status = "Crashed"
            except: pass
        res.append({"metadata": {"name": d, "namespace": "%s"}, "spec": {"taskType": d}, "status": {"state": status, "exitCode": ec}})
print(json.dumps(res))
'
else
  echo "["
  first=true
  if [ -d "/workspaces/tasks" ]; then
    for d in $(ls -r /workspaces/tasks 2>/dev/null); do
      if [ ! -d "/workspaces/tasks/$d" ]; then continue; fi
      if [ "$first" = true ]; then first=false; else echo ","; fi
      status="Pending"
      ec="null"
      if [ -f "/workspaces/tasks/$d/exit_code" ]; then
        code=$(cat "/workspaces/tasks/$d/exit_code" 2>/dev/null | tr -d "\r\n")
        ec="\"$code\""
        if [ "$code" = "0" ]; then status="Completed"; else status="Failed"; fi
      elif [ -f "/workspaces/tasks/$d/pid" ]; then
        pid=$(cat "/workspaces/tasks/$d/pid" 2>/dev/null | tr -d "\r\n")
        if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then status="Running"; else status="Crashed"; fi
      fi
      printf "{\"metadata\":{\"name\":\"%%s\",\"namespace\":\"%s\"},\"spec\":{\"taskType\":\"%%s\"},\"status\":{\"state\":\"%%s\",\"exitCode\":%%s}}" "$d" "$d" "$status" "$ec"
    done
  fi
  echo "]"
fi`, namespace, namespace)

	execOpts := sandbox.ExecOptions{
		Command: []string{"/bin/sh"},
		Stdin:   []byte(peekScript),
		Stdout:  &stdout,
	}
	if err := sandbox.ExecInPod(c.Request.Context(), s.K8sManager.KubeClient, *podID, execOpts); err != nil {
		stdout.Reset()
		execOpts.Command = []string{"/bin/bash"}
		if err2 := sandbox.ExecInPod(c.Request.Context(), s.K8sManager.KubeClient, *podID, execOpts); err2 != nil {
			klog.Warningf("Failed to peek tasks inside pod %s/%s: %v / %v", namespace, podID.Name, err, err2)
			c.JSON(http.StatusOK, []interface{}{})
			return
		}
	}

	var peekedTasks []interface{}
	if err := json.Unmarshal(stdout.Bytes(), &peekedTasks); err != nil {
		klog.Warningf("Failed to decode peeked tasks JSON from pod %s/%s: %v\nOutput: %s", namespace, podID.Name, err, stdout.String())
		c.JSON(http.StatusOK, []interface{}{})
		return
	}

	c.JSON(http.StatusOK, peekedTasks)
}

func (s *Server) getOverseerSandboxTaskLogs(c *gin.Context) {
	overseerName := c.Param("name")
	sandboxName := c.Param("sandboxName")
	taskID := c.Param("taskID")
	namespace := fmt.Sprintf("overseer-%s", overseerName)

	serviceName := fmt.Sprintf("%s-lb", sandboxName)
	targetURL := fmt.Sprintf("http://%s.%s.svc.cluster.local:13339", serviceName, namespace)

	client := http.Client{Timeout: 1 * time.Second}
	if resp, err := client.Get(fmt.Sprintf("%s/logs/%s", targetURL, taskID)); err == nil && resp.StatusCode == http.StatusOK {
		defer resp.Body.Close()
		c.Header("Content-Type", "text/plain")
		_, _ = io.Copy(c.Writer, resp.Body)
		return
	}

	podID, err := sandbox.FindSandboxPodInNamespace(c.Request.Context(), sandboxName, namespace)
	if err != nil || podID == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Sandbox pod not found or not reachable via AgentServer"})
		return
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	readCmd := fmt.Sprintf("if [ -f /workspaces/tasks/%s/execution.log ]; then cat /workspaces/tasks/%s/execution.log; elif [ -f /workspaces/tasks/%s/agent-output.txt ]; then cat /workspaces/tasks/%s/agent-output.txt; else echo 'Log file not found.'; fi", taskID, taskID, taskID, taskID)
	execOpts := sandbox.ExecOptions{
		Command: []string{"/bin/sh"},
		Stdin:   []byte(readCmd),
		Stdout:  &stdout,
		Stderr:  &stderr,
	}
	if err := sandbox.ExecInPod(c.Request.Context(), s.K8sManager.KubeClient, *podID, execOpts); err != nil {
		stdout.Reset()
		stderr.Reset()
		execOpts.Command = []string{"/bin/bash"}
		if err2 := sandbox.ExecInPod(c.Request.Context(), s.K8sManager.KubeClient, *podID, execOpts); err2 != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read task log from pod", "details": fmt.Sprintf("%v / %v", err, err2)})
			return
		}
	}

	c.Header("Content-Type", "text/plain")
	_, _ = c.Writer.Write(stdout.Bytes())
}

func (s *Server) getOverseerSandboxTaskTelemetry(c *gin.Context) {
	overseerName := c.Param("name")
	sandboxName := c.Param("sandboxName")
	taskID := c.Param("taskID")
	namespace := fmt.Sprintf("overseer-%s", overseerName)

	serviceName := fmt.Sprintf("%s-lb", sandboxName)
	targetURL := fmt.Sprintf("http://%s.%s.svc.cluster.local:13339", serviceName, namespace)

	client := http.Client{Timeout: 1 * time.Second}
	if resp, err := client.Get(fmt.Sprintf("%s/telemetry/%s", targetURL, taskID)); err == nil && resp.StatusCode == http.StatusOK {
		defer resp.Body.Close()
		c.Header("Content-Type", "application/json")
		_, _ = io.Copy(c.Writer, resp.Body)
		return
	}

	podID, err := sandbox.FindSandboxPodInNamespace(c.Request.Context(), sandboxName, namespace)
	if err != nil || podID == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Sandbox pod not found or not reachable via AgentServer"})
		return
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	readCmd := fmt.Sprintf("if [ -f /workspaces/tasks/%s/tool-telemetry.json ]; then cat /workspaces/tasks/%s/tool-telemetry.json; else echo '{}'; fi", taskID, taskID)
	execOpts := sandbox.ExecOptions{
		Command: []string{"/bin/sh"},
		Stdin:   []byte(readCmd),
		Stdout:  &stdout,
		Stderr:  &stderr,
	}
	if err := sandbox.ExecInPod(c.Request.Context(), s.K8sManager.KubeClient, *podID, execOpts); err != nil {
		stdout.Reset()
		execOpts.Command = []string{"/bin/bash"}
		_ = sandbox.ExecInPod(c.Request.Context(), s.K8sManager.KubeClient, *podID, execOpts)
	}

	c.Header("Content-Type", "application/json")
	_, _ = c.Writer.Write(stdout.Bytes())
}

func (s *Server) getOverseerSandboxLogs(c *gin.Context) {
	overseerName := c.Param("name")
	sandboxName := c.Param("sandboxName")
	namespace := fmt.Sprintf("overseer-%s", overseerName)

	podID, err := sandbox.FindSandboxPodInNamespace(c.Request.Context(), sandboxName, namespace)
	if err != nil || podID == nil {
		pods, listErr := s.K8sManager.Clientset.CoreV1().Pods(namespace).List(c.Request.Context(), v1.ListOptions{
			LabelSelector: fmt.Sprintf("sandbox=%s", sandboxName),
		})
		if listErr != nil || len(pods.Items) == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "Sandbox pod not found"})
			return
		}
		podName := pods.Items[0].Name
		podID = &types.NamespacedName{Namespace: namespace, Name: podName}
	}

	req := s.K8sManager.Clientset.CoreV1().Pods(namespace).GetLogs(podID.Name, &corev1.PodLogOptions{})
	readCloser, err := req.Stream(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to stream sandbox logs", "details": err.Error()})
		return
	}
	defer readCloser.Close()

	c.Header("Content-Type", "text/plain")
	_, _ = io.Copy(c.Writer, readCloser)
}

func (s *Server) deleteOverseerSandbox(c *gin.Context) {
	overseerName := c.Param("name")
	sandboxName := c.Param("sandboxName")
	namespace := fmt.Sprintf("overseer-%s", overseerName)

	err := s.K8sManager.DeleteSandbox(c.Request.Context(), namespace, sandboxName)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete sandbox", "details": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}
