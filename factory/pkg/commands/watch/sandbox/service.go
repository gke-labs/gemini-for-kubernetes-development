// Package sandbox owns the cluster-side view of factory sandboxes for the
// watch daemon.
//
// It exposes two collaborators:
//   - Service: point lookups and probes ("which sandbox would this task run
//     in?", "is that sandbox busy?") used by the scanners and the dispatcher.
//   - Reconciler: an autonomous goroutine that keeps sandbox annotations in
//     sync with the cluster and garbage collects sandboxes that are no longer
//     needed, so those slow cluster calls never block scanning or dispatching.
package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/common"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/envd"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
	factorysandbox "github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/sandbox"
	githubv39 "github.com/google/go-github/v39/github"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/klog/v2"
)

const (
	// annotationLastTaskState records the outcome of the most recent task run in a sandbox.
	annotationLastTaskState = "sandbox.gemini.google.com/last-task-state"
	// annotationLastTaskType records the kind of the most recent task run in a sandbox.
	annotationLastTaskType = "sandbox.gemini.google.com/last-task-type"

	// taskStateRunning marks a sandbox whose task has not reported an outcome yet.
	taskStateRunning = "Running"
	// taskStateCompleted marks a sandbox whose task finished successfully.
	taskStateCompleted = "Completed"
	// taskStateFailed marks a sandbox whose task finished unsuccessfully.
	taskStateFailed = "Failed"
)

// ServiceConfig identifies the cluster namespace and repository a Service operates on.
type ServiceConfig struct {
	// Namespace is the Kubernetes namespace holding the sandboxes.
	Namespace string
	// Owner is the GitHub organization or user owning the watched repository.
	Owner string
	// Repo is the name of the watched repository.
	Repo string
}

// ServiceDeps holds the clients a Service talks to.
type ServiceDeps struct {
	// Kube is the Kubernetes client used to read and mutate sandboxes.
	Kube *clients.KubernetesClient
	// GitHub is used to resolve which sandbox a pull request belongs to.
	GitHub *githubv39.Client
}

// Service answers questions about individual sandboxes: which sandbox a task
// maps onto, whether it is currently executing something, and how many
// sandboxes are busy in total.
//
// Every method tolerates a nil Kubernetes client so that callers running in
// dry-run mode or in tests do not need to provide a cluster.
type Service struct {
	namespace string
	owner     string
	repo      string
	kube      *clients.KubernetesClient
	gh        *githubv39.Client
}

// NewService constructs a Service from its configuration and clients.
func NewService(cfg ServiceConfig, deps ServiceDeps) *Service {
	return &Service{
		namespace: cfg.Namespace,
		owner:     cfg.Owner,
		repo:      cfg.Repo,
		kube:      deps.Kube,
		gh:        deps.GitHub,
	}
}

// Namespace returns the namespace the service operates on.
func (s *Service) Namespace() string {
	return s.namespace
}

// ResolveName returns the name of the sandbox a task of the given type and
// issue/PR number executes in.
//
// Issue and chore tasks prefer an existing workflow sandbox and otherwise fall
// back to the conventional per-issue name. PR tasks prefer a sandbox already
// labeled with the PR, then a sandbox created for one of the issues the PR
// closes (which is aliased to the PR as a side effect), and finally fall back
// to the conventional per-PR name.
func (s *Service) ResolveName(ctx context.Context, taskType api.TaskType, num int) string {
	if taskType == api.TypeIssueFix || taskType == api.TypeAgentChore {
		wfName := fmt.Sprintf("wf-issue-%d", num)
		if s.kube != nil {
			if _, err := s.kube.DynamicClient.Resource(k8s.SandboxGVR).Namespace(s.namespace).Get(ctx, wfName, metav1.GetOptions{}); err == nil {
				return wfName
			}
		}
		return fmt.Sprintf("fix-%s-%d", s.repo, num)
	}

	// For PR tasks, check if there's an existing sandbox with the PR label
	if s.kube != nil {
		listOpts := metav1.ListOptions{
			LabelSelector: fmt.Sprintf("factory.gemini.google.com/pr=%d", num),
		}
		sbs, err := s.kube.DynamicClient.Resource(k8s.SandboxGVR).Namespace(s.namespace).List(ctx, listOpts)
		if err == nil && len(sbs.Items) > 0 {
			return sbs.Items[0].GetName()
		}
	}

	// If no sandbox is labeled with this PR, try to find a matching issue sandbox by checking referenced issues
	if s.kube != nil && s.gh != nil && s.owner != "" {
		pr, _, err := s.gh.PullRequests.Get(ctx, s.owner, s.repo, num)
		if err == nil {
			// Find referenced issue numbers
			referencedIssues := common.GetReferencedIssues(pr)
			for issueNum := range referencedIssues {
				// Check if there is an active/existing sandbox for this issue
				issueSandboxName := fmt.Sprintf("fix-%s-%d", s.repo, issueNum)
				if _, err := s.kube.DynamicClient.Resource(k8s.SandboxGVR).Namespace(s.namespace).Get(ctx, issueSandboxName, metav1.GetOptions{}); err == nil {
					// We found a matching issue sandbox! Alias it to the PR now for future lookups.
					klog.Infof("Self-healing: Found matching issue sandbox '%s' for PR #%d. Aliasing sandbox to PR...", issueSandboxName, num)
					if aliasErr := factorysandbox.AliasSandboxToPR(ctx, s.kube, s.namespace, issueSandboxName, num, pr.GetHTMLURL()); aliasErr != nil {
						klog.Warningf("Failed to dynamically alias sandbox '%s' to PR #%d: %v", issueSandboxName, num, aliasErr)
					}
					return issueSandboxName
				}
			}
		}
	}

	return fmt.Sprintf("factory-pr-%d", num)
}

// List returns every sandbox in the namespace.
func (s *Service) List(ctx context.Context) ([]unstructured.Unstructured, error) {
	if s.kube == nil {
		return nil, nil
	}
	list, err := s.kube.DynamicClient.Resource(k8s.SandboxGVR).Namespace(s.namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing sandboxes in namespace %s: %w", s.namespace, err)
	}
	return list.Items, nil
}

// Delete removes a sandbox and its resources from the cluster.
func (s *Service) Delete(ctx context.Context, name string) error {
	if s.kube == nil {
		return nil
	}
	return k8s.NewManager(s.kube).DeleteSandbox(ctx, s.namespace, name)
}

// Suspend scales a sandbox down to zero replicas.
func (s *Service) Suspend(ctx context.Context, name string) error {
	if s.kube == nil {
		return nil
	}
	return factorysandbox.SuspendSandbox(ctx, s.kube, s.namespace, name)
}

// IsTaskRunning reports whether the named sandbox is currently executing a task.
//
// It is authoritative rather than advisory: when the recorded annotation still
// claims a task is running but the pod has terminated or envd reports an exit
// code, the annotation is corrected as a side effect so subsequent probes are
// cheap.
func (s *Service) IsTaskRunning(ctx context.Context, name string) (bool, error) {
	if s.kube == nil {
		return false, nil
	}

	unstructObj, err := s.kube.DynamicClient.Resource(k8s.SandboxGVR).Namespace(s.namespace).Get(ctx, name, metav1.GetOptions{})
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

	state := annotations[annotationLastTaskState]
	if state != "" && !strings.EqualFold(state, taskStateRunning) {
		return false, nil
	}

	if terminated, err := s.reconcileTerminatedPod(ctx, name, annotations); err != nil {
		klog.Warningf("Failed to inspect pods of sandbox %s: %v", name, err)
	} else if terminated {
		return false, nil
	}

	return s.probeTaskViaEnvd(ctx, name, annotations)
}

// RefreshTaskState re-probes the named sandbox and corrects its recorded task
// state, reporting whether a task is still running.
//
// It is IsTaskRunning under the name that describes why a caller with no
// interest in the answer would call it: the annotation correction is the point,
// and the boolean is incidental.
func (s *Service) RefreshTaskState(ctx context.Context, name string) (bool, error) {
	return s.IsTaskRunning(ctx, name)
}

// reconcileTerminatedPod detects a sandbox whose pods have all terminated while
// its annotation still claims a task is running, corrects the annotation, and
// deletes evicted pods so the controller can recreate them. It reports whether
// the sandbox was found terminated.
func (s *Service) reconcileTerminatedPod(ctx context.Context, name string, annotations map[string]string) (bool, error) {
	if s.kube.Clientset == nil {
		return false, nil
	}

	podList, err := s.kube.Clientset.CoreV1().Pods(s.namespace).List(ctx, metav1.ListOptions{LabelSelector: fmt.Sprintf("sandbox=%s", name)})
	if err != nil {
		return false, err
	}
	if len(podList.Items) == 0 {
		return false, nil
	}

	hasLiveOrPending := false
	var lastFailedPod *corev1.Pod
	for i := range podList.Items {
		pod := &podList.Items[i]
		if pod.DeletionTimestamp != nil {
			continue
		}
		if pod.Status.Phase == corev1.PodRunning || pod.Status.Phase == corev1.PodPending {
			hasLiveOrPending = true
		} else if pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded || strings.EqualFold(pod.Status.Reason, "Evicted") {
			lastFailedPod = pod
		}
	}
	if hasLiveOrPending || lastFailedPod == nil {
		return false, nil
	}

	reason := lastFailedPod.Status.Reason
	if reason == "" {
		reason = string(lastFailedPod.Status.Phase)
	}
	taskState := taskStateFailed
	if lastFailedPod.Status.Phase == corev1.PodSucceeded {
		taskState = taskStateCompleted
	}
	klog.Warningf("Sandbox %s pod is in %s state (reason: %s). Updating sandbox annotation from Running to %s.", name, lastFailedPod.Status.Phase, reason, taskState)
	_ = factorysandbox.UpdateSandboxTaskAnnotation(ctx, s.kube, s.namespace, name, lastTaskType(annotations), taskState)

	if strings.EqualFold(reason, "Evicted") || (lastFailedPod.Status.Phase == corev1.PodFailed && strings.EqualFold(lastFailedPod.Status.Reason, "Evicted")) {
		klog.Infof("Deleting evicted pod %s so controller can recreate it.", lastFailedPod.Name)
		// The delete arbitrates the eviction count: the sandbox reconciler sweeps
		// evicted pods too, so whoever wins the delete is the one that counts the
		// eviction. The loser sees NotFound and leaves the count alone.
		if err := s.kube.Clientset.CoreV1().Pods(s.namespace).Delete(ctx, lastFailedPod.Name, metav1.DeleteOptions{}); err != nil {
			if !apierrors.IsNotFound(err) {
				klog.Warningf("Failed to delete evicted pod %s: %v", lastFailedPod.Name, err)
			}
		} else {
			_ = factorysandbox.IncrementSandboxEvictionCount(ctx, s.kube, s.namespace, name)
		}
	}
	return true, nil
}

// probeTaskViaEnvd asks envd inside the sandbox whether the latest task is
// still running, updating the sandbox annotation when it has finished.
// Sandboxes that cannot be probed are assumed to be running.
func (s *Service) probeTaskViaEnvd(ctx context.Context, name string, annotations map[string]string) (bool, error) {
	client, err := envd.Connect(ctx, s.namespace, name)
	if err != nil {
		if strings.Contains(err.Error(), "cannot connect to terminated pod") || strings.Contains(err.Error(), "is in Failed state") {
			klog.Warningf("Sandbox %s pod cannot be connected (%v). Updating sandbox annotation to Failed.", name, err)
			_ = factorysandbox.UpdateSandboxTaskAnnotation(ctx, s.kube, s.namespace, name, lastTaskType(annotations), taskStateFailed)
			return false, nil
		}
		return true, nil
	}
	defer client.Close()

	// Check exit_code of the latest task, and fallback to checking process viability via PID
	var buf bytes.Buffer
	checkCmd := envd.BuildCheckLatestTaskStatusCmd(envd.DefaultTasksDir)
	if err := client.Exec(ctx, checkCmd, "/workspaces", nil, nil, &buf, nil); err != nil {
		return true, nil
	}

	switch exitStr := strings.TrimSpace(buf.String()); exitStr {
	case "":
		return true, nil
	case "NOTASKS":
		return false, nil
	case "RUNNING":
		return true, nil
	default:
		// Task has finished!
		taskState := taskStateCompleted
		if exitStr != "0" {
			taskState = taskStateFailed
		}
		taskType := lastTaskType(annotations)
		klog.Infof("Detected completed task %s inside sandbox %s with exit code %s. Updating sandbox annotation to %s.", taskType, name, exitStr, taskState)
		_ = factorysandbox.UpdateSandboxTaskAnnotation(ctx, s.kube, s.namespace, name, taskType, taskState)
		return false, nil
	}
}

// IsTaskCompleted reports whether the named sandbox has already completed a
// task of the given type, which is how a recovered task is recognised as
// finished after a watcher restart.
func (s *Service) IsTaskCompleted(ctx context.Context, name string, taskType api.TaskType) (bool, error) {
	if s.kube == nil {
		return false, nil
	}

	unstructObj, err := s.kube.DynamicClient.Resource(k8s.SandboxGVR).Namespace(s.namespace).Get(ctx, name, metav1.GetOptions{})
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

	state := annotations[annotationLastTaskState]
	recordedType := annotations[annotationLastTaskType]
	return strings.EqualFold(state, taskStateCompleted) && strings.EqualFold(recordedType, sandboxTaskType(taskType)), nil
}

// CountRunningTasks returns the number of sandboxes currently executing a task,
// excluding the sandbox the watcher itself may be running in and suspended sandboxes.
func (s *Service) CountRunningTasks(ctx context.Context) (int, error) {
	if s.kube == nil {
		return 0, nil
	}

	items, err := s.List(ctx)
	if err != nil {
		return 0, err
	}

	count := 0
	for _, item := range items {
		if isSuspended(&item) {
			continue
		}

		if annotations := item.GetAnnotations(); annotations != nil {
			state := annotations[annotationLastTaskState]
			if state != "" && !strings.EqualFold(state, taskStateRunning) {
				continue
			}
		}

		if factorysandbox.IsCurrentSandbox(ctx, s.kube, &item, s.namespace) {
			continue
		}
		count++
	}
	return count, nil
}

// isSuspended reports whether a sandbox has been scaled down to zero replicas.
func isSuspended(item *unstructured.Unstructured) bool {
	spec, ok := item.Object["spec"].(map[string]interface{})
	if !ok {
		return false
	}
	switch r := spec["replicas"].(type) {
	case int64:
		return r == 0
	case float64:
		return int64(r) == 0
	case int:
		return r == 0
	default:
		return false
	}
}

// lastTaskType returns the task type recorded on a sandbox, defaulting to a
// generic label when the annotation is missing.
func lastTaskType(annotations map[string]string) string {
	if taskType := annotations[annotationLastTaskType]; taskType != "" {
		return taskType
	}
	return "task"
}

// sandboxTaskType maps a queue task type onto the task type recorded in sandbox annotations.
func sandboxTaskType(taskType api.TaskType) string {
	switch taskType {
	case api.TypeIssueFix:
		return "fix-issue"
	case api.TypeAgentChore:
		return "agent"
	case api.TypePRComments:
		return "address-comments"
	case api.TypePRInvestigate:
		return "investigate"
	case api.TypePRIterate:
		return "iterate"
	case api.TypePRReview:
		return "review"
	default:
		return string(taskType)
	}
}
