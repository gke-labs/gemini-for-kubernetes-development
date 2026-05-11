package api

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"k8s.io/klog/v2"
)

func (s *Server) getTraceabilityFooter(ctx context.Context, currentBody, namespace, sandboxName, repowatchName, taskType string) string {
	if !s.TraceabilityEnabled || strings.Contains(currentBody, "<!-- repo-agent-metadata") {
		return ""
	}

	log := klog.FromContext(ctx)
	footer := fmt.Sprintf("\n\n---\n<!-- repo-agent-metadata\nsandbox: %s\nrepowatch: %s\ntask-type: %s\ntimestamp: %s",
		sandboxName, repowatchName, taskType, time.Now().UTC().Format(time.RFC3339))

	// Try to find the latest task for this sandbox to include its name and UID
	if sandboxName != "" {
		taskList, err := s.K8sManager.ListSandboxTasks(ctx, namespace, sandboxName)
		if err == nil && len(taskList.Items) > 0 {
			// Sort tasks by creation timestamp to find the latest one
			sort.Slice(taskList.Items, func(i, j int) bool {
				return taskList.Items[i].CreationTimestamp.Before(&taskList.Items[j].CreationTimestamp)
			})
			latestTask := taskList.Items[len(taskList.Items)-1]
			footer += fmt.Sprintf("\nsandbox-task: %s/%s\nsandbox-task-uid: %s",
				latestTask.GetNamespace(), latestTask.GetName(), string(latestTask.GetUID()))
		} else if err != nil {
			log.V(2).Info("Failed to list tasks for traceability metadata", "err", err)
		}
	}

	footer += "\n-->"
	return footer
}
