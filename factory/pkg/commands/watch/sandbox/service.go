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
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/common"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/recipe"
	factorysandbox "github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/sandbox"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/spool"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/taskapi"
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
	// labelPR names the pull request a sandbox serves. An issue sandbox
	// carries it once a PR closing that issue starts reusing it.
	labelPR = "factory.gemini.google.com/pr"

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

// ResolveTaskName is the sandbox task runs in: ResolveName's, except for
// an agent chore, which runs in its agent's own sandbox (factory agent
// create), named by its definition and session rather than its type and
// number: agent-<repo>-<agent>, or a workflow's wf-issue-<n>.
func (s *Service) ResolveTaskName(ctx context.Context, task *api.QueueTask) string {
	if task.Type != api.TypeAgentChore {
		return s.ResolveName(ctx, task.Type, task.Recipe, task.Number)
	}
	name := task.AgentName
	if name == "" && task.SessionID == "" {
		// Queued before tasks carried it: the definition's file name,
		// which is usually its name.
		base := path.Base(task.AgentFile)
		name = strings.TrimSuffix(base, path.Ext(base))
	}
	sandboxName, _ := factorysandbox.AgentSandboxName(s.repo, common.AgentTaskID(name, task.SessionID))
	return sandboxName
}

// ResolveName returns the name of the sandbox a task of the given type and
// issue/PR number executes in: the one its factory command picks, so that
// the busy checks and locks are about the sandbox the task really uses.
//
// Issue tasks prefer an existing workflow sandbox and otherwise fall back
// to the conventional per-issue name. A chore's is ResolveTaskName's. A recipe that runs in a sandbox
// of its own (review) gets that one. Other PR tasks, all recipes, prefer
// the PR's fix sandbox, then a sandbox created for one of the issues the
// PR closes (which is aliased to the PR as a side effect), and finally
// fall back to the name `factory recipe` creates: fix-<repo>-<pr>
// (EnsurePRSandbox).
func (s *Service) ResolveName(ctx context.Context, taskType api.TaskType, recipeName string, num int) string {
	if taskType == api.TypeIssueFix || taskType == api.TypeIssueRule {
		wfName := fmt.Sprintf("wf-issue-%d", num)
		if s.kube != nil {
			if _, err := s.kube.DynamicClient.Resource(k8s.SandboxGVR).Namespace(s.namespace).Get(ctx, wfName, metav1.GetOptions{}); err == nil {
				return wfName
			}
		}
		return fmt.Sprintf("fix-%s-%d", s.repo, num)
	}

	// A credentials: clone recipe (review) holds no credentials, so it
	// shares no sandbox with the PR's other tasks: one of its own, named
	// after it, as factory recipe makes it (EnsureRecipeSandbox). A
	// repository's recipe is not read here, and goes by the PR's.
	if _, rec, err := recipe.Builtin(recipeName); err == nil && rec.Credentials == recipe.CredentialsClone {
		return factorysandbox.RecipeSandboxName(s.repo, num, recipeName)
	}

	prURL := fmt.Sprintf("https://github.com/%s/%s/pull/%d", s.owner, s.repo, num)
	if s.kube != nil {
		if name, err := factorysandbox.PRFixSandbox(ctx, s.kube, s.namespace, s.repo, num, prURL); err == nil && name != "" {
			return name
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

	return fmt.Sprintf("fix-%s-%d", s.repo, num)
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

// Exists reports whether the named sandbox is present in the cluster, whatever
// its state: a suspended sandbox exists, it is merely scaled to zero.
func (s *Service) Exists(ctx context.Context, name string) (bool, error) {
	if s.kube == nil {
		return false, nil
	}
	_, err := s.kube.DynamicClient.Resource(k8s.SandboxGVR).Namespace(s.namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// RequiresHuman is why the named sandbox needs a person before anything
// runs in it again (factorysandbox.AnnotationRequiresHuman), or "": also
// for a sandbox that does not exist.
func (s *Service) RequiresHuman(ctx context.Context, name string) (string, error) {
	if s.kube == nil {
		return "", nil
	}
	sb, err := s.kube.DynamicClient.Resource(k8s.SandboxGVR).Namespace(s.namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return factorysandbox.RequiresHuman(sb.GetAnnotations()), nil
}

// IsTaskRunning reports whether the named sandbox is currently executing a task.
//
// It is authoritative rather than advisory: when the recorded annotation still
// claims a task is running but the pod has terminated or the task server
// reports an exit code, the annotation is corrected as a side effect so subsequent probes are
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

	return s.probeTask(ctx, name, annotations)
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

// taskServer is what the watch reads a sandbox's tasks through: its task
// server (taskapi.ServerSandbox), which lists every task directory newest
// first, a classic command's or a recipe's, marking a task whose process
// is gone with 137, and serves their files.
type taskServer interface {
	List(ctx context.Context) ([]spool.Entry, error)
	ReadFile(ctx context.Context, id, name string) ([]byte, error)
	Close()
}

// connectTasks reaches a sandbox's task server; a var for the tests.
var connectTasks = func(ctx context.Context, namespace, name string) (taskServer, error) {
	return taskapi.ConnectServer(ctx, namespace, name)
}

// probeTask asks the sandbox's task server whether its latest task is
// still running, updating the sandbox annotation when it has finished.
// Sandboxes that cannot be probed are assumed to be running.
func (s *Service) probeTask(ctx context.Context, name string, annotations map[string]string) (bool, error) {
	sb, err := connectTasks(ctx, s.namespace, name)
	if err != nil {
		if strings.Contains(err.Error(), "cannot connect to terminated pod") || strings.Contains(err.Error(), "is in Failed state") {
			klog.Warningf("Sandbox %s pod cannot be connected (%v). Updating sandbox annotation to Failed.", name, err)
			_ = factorysandbox.UpdateSandboxTaskAnnotation(ctx, s.kube, s.namespace, name, lastTaskType(annotations), taskStateFailed)
			return false, nil
		}
		return true, nil
	}
	defer sb.Close()

	entries, err := sb.List(ctx)
	if err != nil {
		return true, nil
	}
	if len(entries) == 0 {
		return false, nil
	}
	latest := entries[0]
	if latest.State != spool.Exited {
		return true, nil
	}
	// Task has finished!
	taskState := taskStateCompleted
	if latest.ExitCode != "0" {
		taskState = taskStateFailed
	}
	taskType := lastTaskType(annotations)
	klog.Infof("Detected completed task %s inside sandbox %s with exit code %s. Updating sandbox annotation to %s.", taskType, name, latest.ExitCode, taskState)
	_ = factorysandbox.UpdateSandboxTaskAnnotation(ctx, s.kube, s.namespace, name, taskType, taskState)
	return false, nil
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
