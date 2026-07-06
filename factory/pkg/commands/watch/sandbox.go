package watch

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/envd"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
	factorysandbox "github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/sandbox"
	githubv39 "github.com/google/go-github/v39/github"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

var envdSandboxTaskExecutor = func(ctx context.Context, namespace, name string) (string, error) {
	client, err := envd.Connect(ctx, namespace, name)
	if err != nil {
		return "", err
	}
	defer client.Close()
	var buf bytes.Buffer
	checkCmd := `task_dir=$(ls -td /workspaces/tasks/* 2>/dev/null | head -1)
	if [ -f "$task_dir/exit_code" ]; then
		cat "$task_dir/exit_code"
	else
		pid=$(cat "$task_dir/pid" 2>/dev/null)
		if [ -n "$pid" ] && ! kill -0 "$pid" 2>/dev/null; then
			echo "137" # Report SIGKILL/Crashed fallback exit code
		fi
	fi`
	if err := client.Exec(ctx, checkCmd, "/workspaces", nil, nil, &buf, nil); err != nil {
		return "", err
	}
	return strings.TrimSpace(buf.String()), nil
}

func resolveSandboxName(ctx context.Context, kubeClient *clients.KubernetesClient, ghClient *githubv39.Client, taskType string, num int, owner, repo, namespace string) string {
	if taskType == "issue-fix" || taskType == "agent-chore" {
		wfName := fmt.Sprintf("wf-issue-%d", num)
		if kubeClient != nil {
			if _, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Get(ctx, wfName, metav1.GetOptions{}); err == nil {
				return wfName
			}
		}
		return fmt.Sprintf("fix-%s-%d", repo, num)
	}

	// For PR tasks, check if there's an existing sandbox with the PR label
	if kubeClient != nil {
		listOpts := metav1.ListOptions{
			LabelSelector: fmt.Sprintf("factory.gemini.google.com/pr=%d", num),
		}
		sbs, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).List(ctx, listOpts)
		if err == nil && len(sbs.Items) > 0 {
			return sbs.Items[0].GetName()
		}
	}

	// If no sandbox is labeled with this PR, try to find a matching issue sandbox by checking referenced issues
	if kubeClient != nil && ghClient != nil && owner != "" {
		pr, _, err := ghClient.PullRequests.Get(ctx, owner, repo, num)
		if err == nil {
			// Find referenced issue numbers
			referencedIssues := getReferencedIssues(pr)
			for issueNum := range referencedIssues {
				// Check if there is an active/existing sandbox for this issue
				issueSandboxName := fmt.Sprintf("fix-%s-%d", repo, issueNum)
				if _, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Get(ctx, issueSandboxName, metav1.GetOptions{}); err == nil {
					// We found a matching issue sandbox! Alias it to the PR now for future lookups.
					klog.Infof("Self-healing: Found matching issue sandbox '%s' for PR #%d. Aliasing sandbox to PR...", issueSandboxName, num)
					if aliasErr := factorysandbox.AliasSandboxToPR(ctx, kubeClient, namespace, issueSandboxName, num, pr.GetHTMLURL()); aliasErr != nil {
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
		// Verify if the task has actually finished by connecting to the sandbox via envd
		exitStr, err := envdSandboxTaskExecutor(ctx, namespace, name)
		if err == nil && exitStr != "" {
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
	if taskType == "issue-fix" {
		sbTaskType = "fix-issue"
	} else if taskType == "agent-chore" {
		sbTaskType = "agent"
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
		if strings.HasPrefix(item.GetName(), "overseer-") {
			continue
		}
		labels := item.GetLabels()
		if labels != nil && labels["overseer.gemini.google.com/overseer"] != "" {
			continue
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

func cleanupClosedPRSandboxes(ctx context.Context, ghClient *githubv39.Client, kubeClient *clients.KubernetesClient, owner, repo, namespace string, dryRun bool) error {
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

		// Fetch the PR state from GitHub
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
			if err := manager.DeleteSandbox(ctx, namespace, name); err != nil {
				klog.Errorf("Failed to delete sandbox '%s' for closed PR #%d: %v", name, num, err)
			}
		}
	}
	return nil
}

func cleanupClosedIssueSandboxes(ctx context.Context, ghClient *githubv39.Client, kubeClient *clients.KubernetesClient, owner, repo, namespace string, dryRun bool) error {
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

		if !isIssueSandbox {
			continue
		}

		// Fetch the issue state from GitHub
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
			if err := manager.DeleteSandbox(ctx, namespace, name); err != nil {
				klog.Errorf("Failed to delete sandbox '%s' for closed issue #%d: %v", name, num, err)
			}
		}
	}
	return nil
}
