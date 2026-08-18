package watch

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/common"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/envd"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
	factorysandbox "github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/sandbox"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/usagereport"
	githubv39 "github.com/google/go-github/v39/github"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

func (w *Watcher) resolveSandboxName(ctx context.Context, kubeClient *clients.KubernetesClient, ghClient *githubv39.Client, taskType string, num int) string {
	if taskType == "issue-fix" || taskType == "agent-chore" {
		wfName := fmt.Sprintf("wf-issue-%d", num)
		if kubeClient != nil {
			if _, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(w.Namespace).Get(ctx, wfName, metav1.GetOptions{}); err == nil {
				return wfName
			}
		}
		return fmt.Sprintf("fix-%s-%d", w.Repo.Repo, num)
	}

	// For PR tasks, check if there's an existing sandbox with the PR label
	if kubeClient != nil {
		listOpts := metav1.ListOptions{
			LabelSelector: fmt.Sprintf("factory.gemini.google.com/pr=%d", num),
		}
		sbs, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(w.Namespace).List(ctx, listOpts)
		if err == nil && len(sbs.Items) > 0 {
			return sbs.Items[0].GetName()
		}
	}

	// If no sandbox is labeled with this PR, try to find a matching issue sandbox by checking referenced issues
	if kubeClient != nil && ghClient != nil && w.Repo.Owner != "" {
		pr, _, err := ghClient.PullRequests.Get(ctx, w.Repo.Owner, w.Repo.Repo, num)
		if err == nil {
			// Find referenced issue numbers
			referencedIssues := common.GetReferencedIssues(pr)
			for issueNum := range referencedIssues {
				// Check if there is an active/existing sandbox for this issue
				issueSandboxName := fmt.Sprintf("fix-%s-%d", w.Repo.Repo, issueNum)
				if _, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(w.Namespace).Get(ctx, issueSandboxName, metav1.GetOptions{}); err == nil {
					// We found a matching issue sandbox! Alias it to the PR now for future lookups.
					klog.Infof("Self-healing: Found matching issue sandbox '%s' for PR #%d. Aliasing sandbox to PR...", issueSandboxName, num)
					if aliasErr := factorysandbox.AliasSandboxToPR(ctx, kubeClient, w.Namespace, issueSandboxName, num, pr.GetHTMLURL()); aliasErr != nil {
						klog.Warningf("Failed to dynamically alias sandbox '%s' to PR #%d: %v", issueSandboxName, num, aliasErr)
					}
					return issueSandboxName
				}
			}
		}
	}

	return fmt.Sprintf("factory-pr-%d", num)
}

func isSandboxTaskRunning(ctx context.Context, kubeClient *clients.KubernetesClient, namespace, name string) (bool, error) {
	unstructObj, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return false, nil
		}
		return false, err
	}

	annotations := unstructObj.GetAnnotations()
	if annotations == nil {
		return true, nil
	}

	state := annotations["sandbox.gemini.google.com/last-task-state"]
	if state == "" || strings.EqualFold(state, "Running") {
		// First check if the sandbox pod has failed or been evicted before calling envd.Connect
		podList, err := kubeClient.Clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: fmt.Sprintf("sandbox=%s", name)})
		if err == nil && len(podList.Items) > 0 {
			hasLiveOrPending := false
			var lastFailedPod *corev1.Pod
			for i := range podList.Items {
				pod := &podList.Items[i]
				if pod.DeletionTimestamp == nil {
					if pod.Status.Phase == corev1.PodRunning || pod.Status.Phase == corev1.PodPending {
						hasLiveOrPending = true
					} else if pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded || strings.EqualFold(pod.Status.Reason, "Evicted") {
						lastFailedPod = pod
					}
				}
			}
			if !hasLiveOrPending && lastFailedPod != nil {
				reason := lastFailedPod.Status.Reason
				if reason == "" {
					reason = string(lastFailedPod.Status.Phase)
				}
				taskState := "Failed"
				if lastFailedPod.Status.Phase == corev1.PodSucceeded {
					taskState = "Completed"
				}
				taskType := annotations["sandbox.gemini.google.com/last-task-type"]
				if taskType == "" {
					taskType = "task"
				}
				klog.Warningf("Sandbox %s pod is in %s state (reason: %s). Updating sandbox annotation from Running to %s.", name, lastFailedPod.Status.Phase, reason, taskState)
				_ = factorysandbox.UpdateSandboxTaskAnnotation(ctx, kubeClient, namespace, name, taskType, taskState)
				if strings.EqualFold(reason, "Evicted") || (lastFailedPod.Status.Phase == corev1.PodFailed && strings.EqualFold(lastFailedPod.Status.Reason, "Evicted")) {
					klog.Infof("Deleting evicted pod %s so controller can recreate it.", lastFailedPod.Name)
					_ = factorysandbox.IncrementSandboxEvictionCount(ctx, kubeClient, namespace, name)
					_ = kubeClient.Clientset.CoreV1().Pods(namespace).Delete(ctx, lastFailedPod.Name, metav1.DeleteOptions{})
				}
				return false, nil
			}
		}

		// Verify if the task has actually finished by connecting to the sandbox via envd
		client, err := envd.Connect(ctx, namespace, name)
		if err == nil {
			defer client.Close()
			var buf bytes.Buffer
			// Check exit_code of the latest task, and fallback to checking process viability via PID
			checkCmd := `task_dir=$(ls -td /workspaces/tasks/* 2>/dev/null | head -1)
			if [ -z "$task_dir" ]; then
				echo "NOTASKS"
			elif [ -s "$task_dir/exit_code" ]; then
				cat "$task_dir/exit_code"
			else
				pid=$(cat "$task_dir/pid" 2>/dev/null)
				if [ -n "$pid" ]; then
					stat=$(ps -o stat= -p "$pid" 2>/dev/null | cut -c 1)
					if ! kill -0 "$pid" 2>/dev/null || [ "$stat" = "Z" ]; then
						echo "137" # Report SIGKILL/Crashed/Zombie fallback exit code
					else
						echo "RUNNING"
					fi
				else
					echo "NOTASKS"
				fi
			fi`
			if err := client.Exec(ctx, checkCmd, "/workspaces", nil, nil, &buf, nil); err == nil {
				exitStr := strings.TrimSpace(buf.String())
				if exitStr == "NOTASKS" {
					return false, nil
				} else if exitStr == "RUNNING" {
					return true, nil
				} else if exitStr != "" {
					// Task has finished!
					taskState := "Completed"
					if exitStr != "0" {
						taskState = "Failed"
					}
					taskType := annotations["sandbox.gemini.google.com/last-task-type"]
					if taskType == "" {
						taskType = "task"
					}
					klog.Infof("Detected completed task %s inside sandbox %s with exit code %s. Updating sandbox annotation to %s.", taskType, name, exitStr, taskState)
					_ = factorysandbox.UpdateSandboxTaskAnnotation(ctx, kubeClient, namespace, name, taskType, taskState)
					return false, nil
				}
			}
		} else if strings.Contains(err.Error(), "cannot connect to terminated pod") || strings.Contains(err.Error(), "is in Failed state") {
			taskType := annotations["sandbox.gemini.google.com/last-task-type"]
			if taskType == "" {
				taskType = "task"
			}
			klog.Warningf("Sandbox %s pod cannot be connected (%v). Updating sandbox annotation to Failed.", name, err)
			_ = factorysandbox.UpdateSandboxTaskAnnotation(ctx, kubeClient, namespace, name, taskType, "Failed")
			return false, nil
		}
		return true, nil
	}

	return false, nil
}

func isSandboxTaskCompleted(ctx context.Context, kubeClient *clients.KubernetesClient, namespace, name, taskType string) (bool, error) {
	unstructObj, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return false, nil
		}
		return false, err
	}

	annotations := unstructObj.GetAnnotations()
	if annotations == nil {
		return false, nil
	}

	state := annotations["sandbox.gemini.google.com/last-task-state"]
	tType := annotations["sandbox.gemini.google.com/last-task-type"]

	sbTaskType := taskType
	switch taskType {
	case "issue-fix":
		sbTaskType = "fix-issue"
	case "agent-chore":
		sbTaskType = "agent"
	case "pr-comments":
		sbTaskType = "address-comments"
	case "pr-investigate":
		sbTaskType = "investigate"
	case "pr-iterate":
		sbTaskType = "iterate"
	case "pr-review":
		sbTaskType = "review"
	}

	if strings.EqualFold(state, "Completed") && strings.EqualFold(tType, sbTaskType) {
		return true, nil
	}
	return false, nil
}

func countRunningSandboxTasks(ctx context.Context, kubeClient *clients.KubernetesClient, namespace string) (int, error) {
	list, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return 0, err
	}

	count := 0
	for _, item := range list.Items {
		if factorysandbox.IsCurrentSandbox(ctx, kubeClient, &item, namespace) {
			continue
		}

		if spec, ok := item.Object["spec"].(map[string]interface{}); ok {
			if r, ok := spec["replicas"].(int64); ok && r == 0 {
				continue
			}
			if r, ok := spec["replicas"].(float64); ok && int64(r) == 0 {
				continue
			}
			if r, ok := spec["replicas"].(int); ok && r == 0 {
				continue
			}
		}

		annotations := item.GetAnnotations()
		if annotations == nil {
			count++
			continue
		}

		state := annotations["sandbox.gemini.google.com/last-task-state"]
		if state == "" || strings.EqualFold(state, "Running") {
			count++
		}
	}
	return count, nil
}

func reconcileRunningSandboxes(ctx context.Context, kubeClient *clients.KubernetesClient, namespace string) {
	if kubeClient == nil {
		return
	}
	list, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		klog.Warningf("Failed to list sandboxes during reconcileRunningSandboxes: %v", err)
		return
	}
	for _, item := range list.Items {
		if factorysandbox.IsCurrentSandbox(ctx, kubeClient, &item, namespace) {
			continue
		}
		annotations := item.GetAnnotations()
		state := ""
		if annotations != nil {
			state = annotations["sandbox.gemini.google.com/last-task-state"]
		}
		if state == "" || strings.EqualFold(state, "Running") {
			sbName := item.GetName()
			running, err := isSandboxTaskRunning(ctx, kubeClient, namespace, sbName)
			if err != nil {
				klog.Warningf("Failed to check status of running sandbox %s during reconcile: %v", sbName, err)
			} else if !running {
				klog.Infof("Sandbox '%s' task completed or stopped running; annotation updated.", sbName)
			}
		}
	}
}

func cleanupClosedPRSandboxes(ctx context.Context, ghClient *githubv39.Client, kubeClient *clients.KubernetesClient, owner, repo, namespace string, openPRs map[int]bool, dryRun bool) error {
	list, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("listing sandboxes for cleanup: %w", err)
	}

	manager := k8s.NewManager(kubeClient)
	for _, item := range list.Items {
		name := item.GetName()
		if !strings.HasPrefix(name, "factory-pr-") {
			continue
		}
		numStr := strings.TrimPrefix(name, "factory-pr-")
		num, err := strconv.Atoi(numStr)
		if err != nil {
			continue
		}

		// Fast-path: If PR is known to be open from Phase 1 scan, skip network call
		if openPRs != nil && openPRs[num] {
			continue
		}

		// Fetch the PR state from GitHub for unconfirmed PRs
		pr, _, err := ghClient.PullRequests.Get(ctx, owner, repo, num)
		if err != nil {
			klog.Warningf("Failed to fetch PR #%d for sandbox cleanup check: %v", num, err)
			continue
		}

		// Check if it is closed or merged
		if pr.GetState() == "closed" {
			klog.Infof("Pull Request #%d is closed/merged. Deleting corresponding sandbox '%s'...", num, name)
			if dryRun {
				fmt.Printf("[DRYRUN] Would delete sandbox '%s' for closed PR #%d\n", name, num)
				continue
			}
			meta := common.SandboxUsageMeta(&item, owner+"/"+repo)
			meta.PR = num
			meta.Issues = common.ReferencedIssueList(pr)
			usagereport.HarvestSandbox(ctx, namespace, name, meta)
			usagereport.ReportPRSubject(ctx, owner+"/"+repo, pr)
			if err := manager.DeleteSandbox(ctx, namespace, name); err != nil {
				klog.Errorf("Failed to delete sandbox '%s' for closed PR #%d: %v", name, num, err)
			}
		}
	}
	return nil
}

func cleanupClosedIssueSandboxes(ctx context.Context, ghClient *githubv39.Client, kubeClient *clients.KubernetesClient, owner, repo, namespace string, openIssues map[int]bool, dryRun bool) error {
	list, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("listing sandboxes for issue cleanup: %w", err)
	}

	manager := k8s.NewManager(kubeClient)
	for _, item := range list.Items {
		name := item.GetName()
		var num int
		var isIssueSandbox bool

		if strings.HasPrefix(name, "wf-issue-") {
			numStr := strings.TrimPrefix(name, "wf-issue-")
			if n, err := strconv.Atoi(numStr); err == nil {
				num = n
				isIssueSandbox = true
			}
		} else if strings.HasPrefix(name, "fix-") {
			idx := strings.LastIndex(name, "-")
			if idx != -1 {
				numStr := name[idx+1:]
				if n, err := strconv.Atoi(numStr); err == nil {
					num = n
					isIssueSandbox = true
				}
			}
		}

		if !isIssueSandbox || num == 0 {
			continue
		}

		// Fast-path: If issue is known to be open from Phase 1 scan, skip network call
		if openIssues != nil && openIssues[num] {
			continue
		}

		// Fetch the issue state from GitHub for unconfirmed issues
		issue, _, err := ghClient.Issues.Get(ctx, owner, repo, num)
		if err != nil {
			klog.Warningf("Failed to fetch issue #%d for sandbox cleanup check: %v", num, err)
			continue
		}

		// Check if the issue is closed
		if issue.GetState() == "closed" {
			klog.Infof("Issue #%d is closed. Deleting corresponding sandbox '%s'...", num, name)
			if dryRun {
				fmt.Printf("[DRYRUN] Would delete sandbox '%s' for closed issue #%d\n", name, num)
				continue
			}
			meta := common.SandboxUsageMeta(&item, owner+"/"+repo)
			meta.Issue = num
			usagereport.HarvestSandbox(ctx, namespace, name, meta)
			usagereport.ReportIssueSubject(ctx, owner+"/"+repo, issue)
			if strings.HasPrefix(name, "wf-issue-") {
				usagereport.PostWorkflowSummaryIfNeeded(ctx, ghClient, owner, repo, num)
			}
			if err := manager.DeleteSandbox(ctx, namespace, name); err != nil {
				klog.Errorf("Failed to delete sandbox '%s' for closed issue #%d: %v", name, num, err)
			}
		}
	}
	return nil
}

func cleanupStaleIdleSandboxes(ctx context.Context, kubeClient *clients.KubernetesClient, repoFullName string, namespace string, ageStr string, dryRun bool) error {
	if kubeClient == nil {
		return nil
	}

	evictionAge, err := parseEvictionAge(ageStr)
	if err != nil {
		return fmt.Errorf("parsing sandbox eviction age: %w", err)
	}

	list, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("listing sandboxes for idle cleanup: %w", err)
	}

	manager := k8s.NewManager(kubeClient)
	now := time.Now()
	for _, item := range list.Items {
		name := item.GetName()
		if factorysandbox.IsCurrentSandbox(ctx, kubeClient, &item, namespace) {
			continue
		}
		creationTime := item.GetCreationTimestamp().Time
		if creationTime.IsZero() {
			continue
		}

		if now.Sub(creationTime) > evictionAge {
			running, err := isSandboxTaskRunning(ctx, kubeClient, namespace, name)
			if err != nil {
				klog.Errorf("Failed to check if sandbox %s is running: %v", name, err)
				continue
			}

			if !running {
				klog.Infof("Sandbox '%s' has been idle and is older than configured eviction age %s (created: %v). Evicting...", name, ageStr, creationTime)
				if dryRun {
					fmt.Printf("[DRYRUN] Would evict stale/idle sandbox '%s'\n", name)
					continue
				}

				usagereport.HarvestSandbox(ctx, namespace, name, common.SandboxUsageMeta(&item, repoFullName))
				if err := manager.DeleteSandbox(ctx, namespace, name); err != nil {
					klog.Errorf("Failed to delete stale/idle sandbox '%s': %v", name, err)
				}
			}
		}
	}

	return nil
}

func parseEvictionAge(ageStr string) (time.Duration, error) {
	ageStr = strings.TrimSpace(ageStr)
	if ageStr == "" {
		return 7 * 24 * time.Hour, nil
	}

	if strings.HasSuffix(ageStr, "d") {
		daysStr := strings.TrimSuffix(ageStr, "d")
		days, err := strconv.Atoi(daysStr)
		if err != nil {
			return 0, fmt.Errorf("invalid days format %q: %w", ageStr, err)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}

	return time.ParseDuration(ageStr)
}
