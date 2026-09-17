package sandbox

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/klog/v2"
)

func fillEnvResources(opts *DevSandboxOptions) {
	if opts.CPURequest == "" {
		opts.CPURequest = os.Getenv("SANDBOX_CPU_REQUEST")
	}
	if opts.CPULimit == "" {
		opts.CPULimit = os.Getenv("SANDBOX_CPU_LIMIT")
	}
	if opts.MemoryRequest == "" {
		opts.MemoryRequest = os.Getenv("SANDBOX_MEMORY_REQUEST")
	}
	if opts.MemoryLimit == "" {
		opts.MemoryLimit = os.Getenv("SANDBOX_MEMORY_LIMIT")
	}
}

func EnsureFixSandbox(ctx context.Context, kubeClient *clients.KubernetesClient, namespace, repoName, taskID, cloneURL, htmlURL, taskTitle, image, diskSize, ephemeralStorage string, secrets []SecretMount, envs []EnvVar, user string) (string, error) {
	name := fmt.Sprintf("fix-%s-%s", repoName, taskID)

	sb, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		labels := sb.GetLabels()
		if labels == nil {
			labels = make(map[string]string)
		}
		if labels["factory.gemini.google.com/user"] != user && user != "" {
			labels["factory.gemini.google.com/user"] = user
			sb.SetLabels(labels)
			_, err = kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Update(ctx, sb, metav1.UpdateOptions{})
			if err != nil {
				klog.Warningf("Failed to update sandbox labels with user '%s': %v", user, err)
			}
		}
		return name, nil
	}
	if !strings.Contains(err.Error(), "not found") {
		return "", fmt.Errorf("checking sandbox existence: %w", err)
	}

	if diskSize == "" {
		diskSize = "10Gi"
	}

	opt := AgentSandboxOptions{
		DevSandboxOptions: DevSandboxOptions{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"sandbox.gemini.google.com/type":    "fix",
				"factory.gemini.google.com/managed": "true",
				"factory.gemini.google.com/user":    user,
			},
			Annotations: map[string]string{
				"repo":     repoName,
				"cloneURL": cloneURL,
				"htmlURL":  htmlURL,
			},
			Image:             image,
			Replicas:          1,
			WorkspaceDiskSize: diskSize,
			EphemeralStorage:  ephemeralStorage,
			Secrets:           secrets,
			Env:               envs,
		},
	}

	fillEnvResources(&opt.DevSandboxOptions)
	sbObj, svc := NewAgentSandbox(opt)

	_, err = kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Create(ctx, sbObj, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("creating sandbox CR: %w", err)
	}

	_, err = kubeClient.Clientset.CoreV1().Services(namespace).Create(ctx, svc, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("creating sandbox service: %w", err)
	}

	return name, nil
}

func EnsureAgentSandbox(ctx context.Context, kubeClient *clients.KubernetesClient, namespace, repoName, taskID, cloneURL, htmlURL, taskTitle, image, diskSize, ephemeralStorage string, secrets []SecretMount, envs []EnvVar, user string) (string, error) {
	name := fmt.Sprintf("agent-%s-%s", repoName, taskID)
	labels := map[string]string{
		"sandbox.gemini.google.com/type":    "agent",
		"factory.gemini.google.com/managed": "true",
		"factory.gemini.google.com/user":    user,
	}

	if idx := strings.Index(taskID, "-issue-"); idx != -1 {
		workflowName := taskID[:idx]
		issueNum := taskID[idx+len("-issue-"):]
		name = fmt.Sprintf("wf-issue-%s", issueNum)
		labels["factory.gemini.google.com/workflow"] = workflowName
	}

	sb, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		labels := sb.GetLabels()
		if labels == nil {
			labels = make(map[string]string)
		}
		if labels["factory.gemini.google.com/user"] != user && user != "" {
			labels["factory.gemini.google.com/user"] = user
			sb.SetLabels(labels)
			_, err = kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Update(ctx, sb, metav1.UpdateOptions{})
			if err != nil {
				klog.Warningf("Failed to update sandbox labels with user '%s': %v", user, err)
			}
		}
		return name, nil
	}
	if !strings.Contains(err.Error(), "not found") {
		return "", fmt.Errorf("checking sandbox existence: %w", err)
	}

	if diskSize == "" {
		diskSize = "10Gi"
	}

	opt := AgentSandboxOptions{
		DevSandboxOptions: DevSandboxOptions{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
			Annotations: map[string]string{
				"repo":     repoName,
				"cloneURL": cloneURL,
				"htmlURL":  htmlURL,
			},
			Image:             image,
			Replicas:          1,
			WorkspaceDiskSize: diskSize,
			EphemeralStorage:  ephemeralStorage,
			Secrets:           secrets,
			Env:               envs,
		},
	}

	fillEnvResources(&opt.DevSandboxOptions)
	sbObj, svc := NewAgentSandbox(opt)

	_, err = kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Create(ctx, sbObj, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("creating sandbox CR: %w", err)
	}

	_, err = kubeClient.Clientset.CoreV1().Services(namespace).Create(ctx, svc, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("creating sandbox service: %w", err)
	}

	return name, nil
}

func EnsureAdoptSandbox(ctx context.Context, kubeClient *clients.KubernetesClient, namespace, repoName string, prNum int, cloneURL, htmlURL, image, diskSize, ephemeralStorage string, secrets []SecretMount, envs []EnvVar, user string) (string, error) {
	name := fmt.Sprintf("adopt-%s-%d", repoName, prNum)

	sb, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		labels := sb.GetLabels()
		if labels == nil {
			labels = make(map[string]string)
		}
		if labels["factory.gemini.google.com/user"] != user && user != "" {
			labels["factory.gemini.google.com/user"] = user
			sb.SetLabels(labels)
			_, err = kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Update(ctx, sb, metav1.UpdateOptions{})
			if err != nil {
				klog.Warningf("Failed to update sandbox labels with user '%s': %v", user, err)
			}
		}
		return name, nil
	}
	if !strings.Contains(err.Error(), "not found") {
		return "", fmt.Errorf("checking sandbox existence: %w", err)
	}

	if diskSize == "" {
		diskSize = "10Gi"
	}

	opt := AgentSandboxOptions{
		DevSandboxOptions: DevSandboxOptions{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"sandbox.gemini.google.com/type":    "adopt",
				"factory.gemini.google.com/managed": "true",
				"factory.gemini.google.com/user":    user,
			},
			Annotations: map[string]string{
				"repo":     repoName,
				"cloneURL": cloneURL,
				"htmlURL":  htmlURL,
			},
			Image:             image,
			Replicas:          1,
			WorkspaceDiskSize: diskSize,
			EphemeralStorage:  ephemeralStorage,
			Secrets:           secrets,
			Env:               envs,
		},
	}

	fillEnvResources(&opt.DevSandboxOptions)
	sbObj, svc := NewAgentSandbox(opt)

	_, err = kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Create(ctx, sbObj, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("creating sandbox CR: %w", err)
	}

	_, err = kubeClient.Clientset.CoreV1().Services(namespace).Create(ctx, svc, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("creating sandbox service: %w", err)
	}

	return name, nil
}

func AliasSandboxToPR(ctx context.Context, kubeClient *clients.KubernetesClient, namespace, sandboxName string, prNum int, prURL string) error {
	unstructObj, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Get(ctx, sandboxName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("getting sandbox %s: %w", sandboxName, err)
	}

	labels := unstructObj.GetLabels()
	if labels == nil {
		labels = make(map[string]string)
	}
	labels["factory.gemini.google.com/pr"] = fmt.Sprintf("%d", prNum)
	unstructObj.SetLabels(labels)

	annotations := unstructObj.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	annotations["pr"] = fmt.Sprintf("%d", prNum)
	if prURL != "" {
		annotations["htmlURL"] = prURL
	}
	unstructObj.SetAnnotations(annotations)

	_, err = kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Update(ctx, unstructObj, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("updating sandbox %s with PR alias: %w", sandboxName, err)
	}
	return nil
}

// ReviewSandboxName is the review sandbox for a PR. PR numbers are only
// unique within a repo, so the repo is part of the name — two repos in the
// same namespace can each carry a PR with the same number.
func ReviewSandboxName(repo string, prNum int) string {
	slug := strings.ToLower(repo)
	var b strings.Builder
	for _, r := range slug {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	slug = strings.Trim(b.String(), "-")
	suffix := fmt.Sprintf("-%d", prNum)
	// The companion "<name>-lb" Service must fit the 63-char DNS label cap.
	if budget := 60 - len("factory-pr-") - len(suffix); len(slug) > budget {
		slug = strings.Trim(slug[:budget], "-")
	}
	return "factory-pr-" + slug + suffix
}

// sandboxBelongsToRepo guards adoption of an existing sandbox found by PR
// number: pre-repo-scoping sandboxes (plain factory-pr-<n> names, pr-number
// label lookups) may belong to a different repo's PR with the same number.
func sandboxBelongsToRepo(sb *unstructured.Unstructured, repo, prHTMLURL string) bool {
	annotations := sb.GetAnnotations()
	if r := annotations["repo"]; r != "" {
		return r == repo
	}
	if u := annotations["htmlURL"]; u != "" {
		return u == prHTMLURL
	}
	return false
}

func ensureSandboxUserLabel(ctx context.Context, kubeClient *clients.KubernetesClient, namespace string, sb *unstructured.Unstructured, user string) {
	labels := sb.GetLabels()
	if labels == nil {
		labels = make(map[string]string)
	}
	if labels["factory.gemini.google.com/user"] != user && user != "" {
		labels["factory.gemini.google.com/user"] = user
		sb.SetLabels(labels)
		_, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Update(ctx, sb, metav1.UpdateOptions{})
		if err != nil {
			klog.Warningf("Failed to update sandbox labels with user '%s': %v", user, err)
		}
	}
}

func EnsureReviewSandbox(ctx context.Context, kubeClient *clients.KubernetesClient, namespace string, prNum int, prTitle, prHTMLURL, prDiffURL, prCloneURL, image, diskSize, ephemeralStorage string, secrets []SecretMount, envs []EnvVar, user string) (string, error) {
	parts := strings.Split(strings.TrimSuffix(prCloneURL, ".git"), "/")
	repo := parts[len(parts)-1]

	listOpts := metav1.ListOptions{
		LabelSelector: fmt.Sprintf("factory.gemini.google.com/pr=%d", prNum),
	}
	sbs, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).List(ctx, listOpts)
	if err == nil && len(sbs.Items) > 0 {
		for i := range sbs.Items {
			sb := &sbs.Items[i]
			if !sandboxBelongsToRepo(sb, repo, prHTMLURL) {
				continue
			}
			ensureSandboxUserLabel(ctx, kubeClient, namespace, sb, user)
			return sb.GetName(), nil
		}
	}

	name := ReviewSandboxName(repo, prNum)

	// The legacy repo-less name is checked too so existing sandboxes keep
	// being reused across the naming change.
	for _, candidate := range []string{name, fmt.Sprintf("factory-pr-%d", prNum)} {
		sbGet, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Get(ctx, candidate, metav1.GetOptions{})
		if err != nil {
			if !strings.Contains(err.Error(), "not found") {
				return "", fmt.Errorf("checking sandbox existence: %w", err)
			}
			continue
		}
		if candidate != name && !sandboxBelongsToRepo(sbGet, repo, prHTMLURL) {
			continue
		}
		ensureSandboxUserLabel(ctx, kubeClient, namespace, sbGet, user)
		return candidate, nil
	}

	if diskSize == "" {
		diskSize = "10Gi"
	}

	opt := ReviewSandboxOptions{
		DevSandboxOptions: DevSandboxOptions{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"sandbox.gemini.google.com/type":    "review",
				"factory.gemini.google.com/managed": "true",
				"factory.gemini.google.com/user":    user,
			},
			Image:             image,
			Replicas:          1,
			WorkspaceDiskSize: diskSize,
			EphemeralStorage:  ephemeralStorage,
			Secrets:           secrets,
			Env:               envs,
		},
		PRNumber:   prNum,
		PRTitle:    prTitle,
		PRHTMLURL:  prHTMLURL,
		PRDiffURL:  prDiffURL,
		PRCloneURL: prCloneURL,
		RepoName:   repo,
	}

	fillEnvResources(&opt.DevSandboxOptions)
	sb, svc := NewReviewSandbox(opt)

	_, err = kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Create(ctx, sb, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("creating review sandbox CR: %w", err)
	}

	_, err = kubeClient.Clientset.CoreV1().Services(namespace).Create(ctx, svc, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("creating review sandbox service: %w", err)
	}

	return name, nil
}

func UpdateSandboxTaskAnnotation(ctx context.Context, kubeClient *clients.KubernetesClient, namespace, sandboxName, taskType, taskState string) error {
	if ctx.Err() != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
	}

	unstructObj, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Get(ctx, sandboxName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("getting sandbox %s: %w", sandboxName, err)
	}

	unpaused := false
	if taskType != "" && taskState != "Completed" && taskState != "Failed" {
		replicas, found, _ := unstructured.NestedInt64(unstructObj.Object, "spec", "replicas")
		if found && replicas == 0 {
			_ = unstructured.SetNestedField(unstructObj.Object, int64(1), "spec", "replicas")
			unpaused = true
		}
	}

	annotations := unstructObj.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	if unpaused {
		annotations["sandbox.gemini.google.com/unpaused-at"] = time.Now().UTC().Format(time.RFC3339)
	}

	if taskType != "" {
		annotations["sandbox.gemini.google.com/last-task-type"] = taskType
		annotations["sandbox.gemini.google.com/last-task-state"] = taskState
		if taskState == "Completed" || taskState == "Failed" {
			nowStr := time.Now().UTC().Format(time.RFC3339)
			annotations["sandbox.gemini.google.com/completion-time"] = nowStr
			annotations["sandbox.gemini.google.com/last-task-time"] = nowStr
		}
	} else {
		delete(annotations, "sandbox.gemini.google.com/last-task-type")
		delete(annotations, "sandbox.gemini.google.com/last-task-state")
	}

	unstructObj.SetAnnotations(annotations)
	_, err = kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Update(ctx, unstructObj, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("updating sandbox %s task annotations: %w", sandboxName, err)
	}
	return nil
}

func IncrementSandboxEvictionCount(ctx context.Context, kubeClient *clients.KubernetesClient, namespace, sandboxName string) error {
	if ctx.Err() != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
	}

	unstructObj, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Get(ctx, sandboxName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("getting sandbox %s: %w", sandboxName, err)
	}

	annotations := unstructObj.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}

	countStr := annotations["sandbox.gemini.google.com/eviction-count"]
	count, _ := strconv.Atoi(countStr)
	count++
	annotations["sandbox.gemini.google.com/eviction-count"] = strconv.Itoa(count)

	unstructObj.SetAnnotations(annotations)
	_, err = kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Update(ctx, unstructObj, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("updating sandbox %s eviction count: %w", sandboxName, err)
	}
	return nil
}

// IsCurrentSandbox checks if the given Sandbox resource corresponds to the pod currently running this process,
// preventing self-suspension or self-eviction of the active watch/controller daemon.
func IsCurrentSandbox(ctx context.Context, kubeClient *clients.KubernetesClient, item *unstructured.Unstructured, namespace string) bool {
	if item == nil {
		return false
	}
	sbName := item.GetName()
	if sbName == "" {
		return false
	}

	// If not running inside a Kubernetes pod (or sandbox container), we are running on an external workstation.
	// Therefore, no cluster sandbox corresponds to "this current process".
	if os.Getenv("KUBERNETES_SERVICE_HOST") == "" && os.Getenv("SANDBOX_NAME") == "" {
		if _, err := os.Stat("/var/run/secrets/kubernetes.io/serviceaccount/token"); os.IsNotExist(err) {
			return false
		}
	}

	// 1. Fast path from explicit environment variables
	if envSB := os.Getenv("SANDBOX_NAME"); envSB != "" && envSB == sbName {
		return true
	}
	podName := os.Getenv("POD_NAME")
	if podName == "" {
		podName = os.Getenv("HOSTNAME")
	}
	if podName != "" {
		// If the pod name exactly equals the sandbox name (e.g. overseer-kcc == overseer-kcc)
		if podName == sbName {
			return true
		}
		// If the pod name starts with the sandbox name followed by a hyphen (e.g. overseer-kcc-6c66b9cb6d-7gprl)
		if strings.HasPrefix(podName, sbName+"-") {
			return true
		}
		// 2. Query k8s API for the current pod's labels and owner references
		if kubeClient != nil && kubeClient.Clientset != nil {
			pod, err := kubeClient.Clientset.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
			if err == nil && pod != nil {
				// Check standard sandbox label on the pod
				if pod.Labels["sandbox"] == sbName || pod.Labels["agents.x-k8s.io/sandbox"] == sbName {
					return true
				}
				// Check owner references pointing to this Sandbox resource
				for _, owner := range pod.OwnerReferences {
					if owner.Name == sbName {
						return true
					}
				}
			}
		}
	}
	return false
}

// SuspendIdleSandboxes scales every sandbox that has been idle for longer than
// idleTimeout down to zero replicas, and returns how many were suspended.
//
// Callers that need to bracket each sandbox with their own bookkeeping, such as
// taking a lease, should drive SuspendSandboxIfIdle themselves instead.
func SuspendIdleSandboxes(ctx context.Context, kubeClient *clients.KubernetesClient, namespace string, idleTimeout time.Duration, dryRun bool) (int, error) {
	if kubeClient == nil || idleTimeout <= 0 {
		return 0, nil
	}

	list, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return 0, fmt.Errorf("listing sandboxes for idle suspension check: %w", err)
	}

	suspendedCount := 0
	for i := range list.Items {
		item := &list.Items[i]
		suspended, err := SuspendSandboxIfIdle(ctx, kubeClient, namespace, item, idleTimeout, dryRun)
		if err != nil {
			klog.Errorf("Failed to suspend idle sandbox '%s': %v", item.GetName(), err)
			continue
		}
		if suspended {
			suspendedCount++
		}
	}

	return suspendedCount, nil
}

// SuspendSandboxIfIdle scales a single sandbox down to zero replicas if it has
// gone without activity for longer than idleTimeout, reporting whether it was
// suspended.
//
// item is the caller's view of the sandbox and is used only to decide cheaply
// whether it is a candidate at all. The sandbox is re-read before being written
// because that view may be stale: a caller sweeping many sandboxes can be
// minutes past its listing by the time it reaches this one, and an Update
// carrying a stale resourceVersion would be rejected. The re-read also gives a
// last chance to notice that the sandbox picked up work in the meantime.
func SuspendSandboxIfIdle(ctx context.Context, kubeClient *clients.KubernetesClient, namespace string, item *unstructured.Unstructured, idleTimeout time.Duration, dryRun bool) (bool, error) {
	if kubeClient == nil || item == nil || idleTimeout <= 0 {
		return false, nil
	}

	name := item.GetName()
	if IsCurrentSandbox(ctx, kubeClient, item, namespace) {
		return false, nil
	}
	lastActivity, idle := idleSince(item, idleTimeout, time.Now())
	if !idle {
		return false, nil
	}

	klog.Infof("Sandbox '%s' in namespace '%s' has not run any task for %v (last activity: %v). Suspending (replicas=0)...", name, namespace, idleTimeout, lastActivity)
	if dryRun {
		fmt.Printf("[DRYRUN] Would suspend idle sandbox '%s' (replicas=0)\n", name)
		return true, nil
	}

	current, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("reading sandbox %s before suspending it: %w", name, err)
	}
	if _, stillIdle := idleSince(current, idleTimeout, time.Now()); !stillIdle {
		klog.Infof("Sandbox '%s' became active while it was being collected; leaving it running.", name)
		return false, nil
	}

	if err := unstructured.SetNestedField(current.Object, int64(0), "spec", "replicas"); err != nil {
		return false, fmt.Errorf("setting replicas=0 on sandbox %s: %w", name, err)
	}
	if _, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Update(ctx, current, metav1.UpdateOptions{}); err != nil {
		return false, fmt.Errorf("updating sandbox %s to replicas=0: %w", name, err)
	}

	fmt.Printf("Suspended idle sandbox '%s' (replicas=0)\n", name)
	return true, nil
}

// idleSince reports when a sandbox was last active and whether that was longer
// ago than idleTimeout.
//
// A sandbox that is already scaled to zero, or whose annotations say a task is
// still running, is never considered idle: the first has nothing left to
// suspend and the second is busy regardless of how old its timestamps look.
func idleSince(item *unstructured.Unstructured, idleTimeout time.Duration, now time.Time) (time.Time, bool) {
	replicas, found, err := unstructured.NestedInt64(item.Object, "spec", "replicas")
	if err == nil && found && replicas == 0 {
		return time.Time{}, false // Already suspended.
	}

	// Last activity is the most recent of the creation time and any of the
	// timestamps a task run leaves behind.
	lastActivity := item.GetCreationTimestamp().Time
	if annotations := item.GetAnnotations(); annotations != nil {
		if state := annotations["sandbox.gemini.google.com/last-task-state"]; state != "" && !strings.EqualFold(state, "Completed") && !strings.EqualFold(state, "Failed") {
			return time.Time{}, false
		}
		for _, key := range []string{
			"sandbox.gemini.google.com/completion-time",
			"sandbox.gemini.google.com/last-task-time",
			"sandbox.gemini.google.com/unpaused-at",
		} {
			tsStr, ok := annotations[key]
			if !ok {
				continue
			}
			if ts, err := time.Parse(time.RFC3339, tsStr); err == nil && ts.After(lastActivity) {
				lastActivity = ts
			}
		}
	}

	return lastActivity, now.Sub(lastActivity) > idleTimeout
}
