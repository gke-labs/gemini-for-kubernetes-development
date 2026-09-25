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
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/klog/v2"
)

// terminating reports whether a sandbox is on its way out. Every
// Ensure*Sandbox reuses an existing sandbox by name or by label, and a
// sandbox with a deletion timestamp answers both lookups while being
// incapable of running anything: its pod is going away and will not
// come back. Returning one leaves the caller waiting for a pod that
// will never be ready, which is an indefinite hang rather than a
// failure — the same shape as the zombie process that once answered
// kill -0.
func terminating(sb *unstructured.Unstructured) bool {
	return sb != nil && sb.GetDeletionTimestamp() != nil
}

// awaitSandboxGone waits for a terminating sandbox to finish leaving,
// so the caller can create a fresh one under the same name. Deleting a
// sandbox and immediately re-running the task that used it is an
// ordinary thing to do, and it should work rather than collide.
func awaitSandboxGone(ctx context.Context, kubeClient *clients.KubernetesClient, namespace, name string) error {
	const (
		timeout = 90 * time.Second
		poll    = 2 * time.Second
	)
	deadline := time.Now().Add(timeout)
	for {
		_, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("waiting for terminating sandbox %s: %w", name, err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("sandbox %s is still terminating after %s; retry once it is gone", name, timeout)
		}
		klog.Infof("sandbox %s is terminating; waiting for it to finish before recreating", name)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
}

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

// ExploreSandboxName returns the per-repo exploration sandbox name
// (explore-<slug>), slug-budgeted for the companion -lb Service's DNS cap.
func ExploreSandboxName(repo string) string {
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
	if budget := 60 - len("explore-"); len(slug) > budget {
		slug = strings.Trim(slug[:budget], "-")
	}
	return "explore-" + slug
}

// RunbookSandboxName is the run environment for one deployment
// instance: runbook-<repo>-<instance>. One sandbox per instance (the
// same runbook deploys many times with different parameters); re-runs
// of an instance reuse it — the PVC holds the deployment's state
// (kubeconfig, built artifacts, the serving process), so the sandbox
// IS the handle to the deployment.
func RunbookSandboxName(repo, instance string) string {
	slugify := func(s string) string {
		s = strings.ToLower(s)
		var b strings.Builder
		for _, r := range s {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
				b.WriteRune(r)
			} else {
				b.WriteRune('-')
			}
		}
		return strings.Trim(b.String(), "-")
	}
	suffix := slugify(instance)
	slug := slugify(repo)
	if budget := 60 - len("runbook-") - len(suffix) - 1; len(slug) > budget {
		slug = strings.Trim(slug[:budget], "-")
	}
	return "runbook-" + slug + "-" + suffix
}

// DeployerServiceAccount is the per-namespace KSA explore sandboxes run
// as. Direct Workload Identity federation makes it a GCP principal
// (principal://…/subject/ns/<ns>/sa/factory-deployer) the member grants roles to
// in their own project — no keys stored anywhere. It carries no
// Kubernetes RBAC; only the explore/deploy surface uses it.
const DeployerServiceAccount = "factory-deployer"

// ensureDeployerServiceAccount creates the deployer KSA if missing.
func ensureDeployerServiceAccount(ctx context.Context, kubeClient *clients.KubernetesClient, namespace string) error {
	_, err := kubeClient.Clientset.CoreV1().ServiceAccounts(namespace).Get(ctx, DeployerServiceAccount, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
		Name:      DeployerServiceAccount,
		Namespace: namespace,
		Labels:    map[string]string{"factory.gemini.google.com/managed": "true"},
	}}
	if _, cerr := kubeClient.Clientset.CoreV1().ServiceAccounts(namespace).Create(ctx, sa, metav1.CreateOptions{}); cerr != nil && !strings.Contains(cerr.Error(), "already exists") {
		return fmt.Errorf("creating %s service account: %w", DeployerServiceAccount, cerr)
	}
	return nil
}

// EnsureExploreSandbox ensures the repo's exploration sandbox: one per
// repo per namespace — the workshop where understanding docs are built
// and interactive exploration sessions live. Reuses the fix sandbox
// conventions (managed label, repo/cloneURL/htmlURL annotations).
func EnsureExploreSandbox(ctx context.Context, kubeClient *clients.KubernetesClient, namespace, repoName, cloneURL, htmlURL, image, diskSize, ephemeralStorage string, secrets []SecretMount, envs []EnvVar, user string) (string, error) {
	name := ExploreSandboxName(repoName)
	if err := ensureDeployerServiceAccount(ctx, kubeClient, namespace); err != nil {
		return "", err
	}

	sb, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	switch {
	case err == nil && terminating(sb):
		// Reusing a sandbox that is being deleted means waiting for a
		// pod that will never be ready. Wait for the name to free up
		// and fall through to creating a fresh one.
		if werr := awaitSandboxGone(ctx, kubeClient, namespace, name); werr != nil {
			return "", werr
		}
	case err == nil:
		ensureSandboxUserLabel(ctx, kubeClient, namespace, sb, user)
		return name, nil
	case !strings.Contains(err.Error(), "not found"):
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
				"sandbox.gemini.google.com/type":    "explore",
				"factory.gemini.google.com/managed": "true",
				"factory.gemini.google.com/user":    user,
			},
			Annotations: map[string]string{
				"repo":     repoName,
				"cloneURL": cloneURL,
				"htmlURL":  htmlURL,
			},
			Image:              image,
			Replicas:           1,
			WorkspaceDiskSize:  diskSize,
			EphemeralStorage:   ephemeralStorage,
			Secrets:            secrets,
			Env:                envs,
			ServiceAccountName: DeployerServiceAccount,
		},
	}

	fillEnvResources(&opt.DevSandboxOptions)
	sbObj, svc := NewAgentSandbox(opt)

	if _, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Create(ctx, sbObj, metav1.CreateOptions{}); err != nil {
		return "", fmt.Errorf("creating sandbox CR: %w", err)
	}
	if _, err := kubeClient.Clientset.CoreV1().Services(namespace).Create(ctx, svc, metav1.CreateOptions{}); err != nil {
		return "", fmt.Errorf("creating sandbox service: %w", err)
	}
	return name, nil
}

// EnsureRunbookSandbox creates (or finds) the run environment for one
// runbook path. Type label "runbook": the board controller excludes these
// from slot counting and idle-pause — a run environment hosts living
// deployments, it is not a task slot.
func EnsureRunbookSandbox(ctx context.Context, kubeClient *clients.KubernetesClient, namespace, repoName, scenario, instance, cloneURL, htmlURL, image, diskSize, ephemeralStorage string, secrets []SecretMount, envs []EnvVar, user string) (string, error) {
	name := RunbookSandboxName(repoName, instance)

	sb, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	switch {
	case err == nil && terminating(sb):
		// Reusing a sandbox that is being deleted means waiting for a
		// pod that will never be ready. Wait for the name to free up
		// and fall through to creating a fresh one.
		if werr := awaitSandboxGone(ctx, kubeClient, namespace, name); werr != nil {
			return "", werr
		}
	case err == nil:
		ensureSandboxUserLabel(ctx, kubeClient, namespace, sb, user)
		return name, nil
	case !strings.Contains(err.Error(), "not found"):
		return "", fmt.Errorf("checking sandbox existence: %w", err)
	}
	if err := ensureDeployerServiceAccount(ctx, kubeClient, namespace); err != nil {
		return "", err
	}

	if diskSize == "" {
		diskSize = "10Gi"
	}

	opt := AgentSandboxOptions{
		DevSandboxOptions: DevSandboxOptions{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"sandbox.gemini.google.com/type":    "runbook",
				"factory.gemini.google.com/managed": "true",
				"factory.gemini.google.com/user":    user,
			},
			Annotations: map[string]string{
				"repo":     repoName,
				"cloneURL": cloneURL,
				"htmlURL":  htmlURL,
				"sandbox.gemini.google.com/runbook-scenario": scenario,
				"sandbox.gemini.google.com/runbook-instance": instance,
			},
			Image:              image,
			Replicas:           1,
			WorkspaceDiskSize:  diskSize,
			EphemeralStorage:   ephemeralStorage,
			Secrets:            secrets,
			Env:                envs,
			ServiceAccountName: DeployerServiceAccount,
		},
	}

	fillEnvResources(&opt.DevSandboxOptions)
	// Run environments build images and compile big module graphs; on
	// Autopilot the pod is entitled by its REQUESTS, so the code-task
	// defaults (500m/2Gi) make builds crawl. Raised only when neither
	// flags, config, nor SANDBOX_* env chose values.
	if opt.CPURequest == "" {
		opt.CPURequest = "2"
	}
	if opt.MemoryRequest == "" {
		opt.MemoryRequest = "4Gi"
	}
	sbObj, svc := NewAgentSandbox(opt)

	if _, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Create(ctx, sbObj, metav1.CreateOptions{}); err != nil {
		return "", fmt.Errorf("creating sandbox CR: %w", err)
	}
	if _, err := kubeClient.Clientset.CoreV1().Services(namespace).Create(ctx, svc, metav1.CreateOptions{}); err != nil {
		return "", fmt.Errorf("creating sandbox service: %w", err)
	}
	return name, nil
}

func EnsureFixSandbox(ctx context.Context, kubeClient *clients.KubernetesClient, namespace, repoName, taskID, cloneURL, htmlURL, taskTitle, image, diskSize, ephemeralStorage string, secrets []SecretMount, envs []EnvVar, user string) (string, error) {
	name := fmt.Sprintf("fix-%s-%s", repoName, taskID)

	sb, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err == nil && terminating(sb) {
		// See awaitSandboxGone: a deleted sandbox still answers Get
		// until its finalizers clear, and handing it back would hang.
		if werr := awaitSandboxGone(ctx, kubeClient, namespace, name); werr != nil {
			return "", werr
		}
	} else if err == nil {
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
	} else if !strings.Contains(err.Error(), "not found") {
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
	if err == nil && terminating(sb) {
		// See awaitSandboxGone: a deleted sandbox still answers Get
		// until its finalizers clear, and handing it back would hang.
		if werr := awaitSandboxGone(ctx, kubeClient, namespace, name); werr != nil {
			return "", werr
		}
	} else if err == nil {
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
	} else if !strings.Contains(err.Error(), "not found") {
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
	if err == nil && terminating(sb) {
		// See awaitSandboxGone: a deleted sandbox still answers Get
		// until its finalizers clear, and handing it back would hang.
		if werr := awaitSandboxGone(ctx, kubeClient, namespace, name); werr != nil {
			return "", werr
		}
	} else if err == nil {
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
	} else if !strings.Contains(err.Error(), "not found") {
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
			// The PR label outlives the sandbox it is on. Deleting a
			// PR's fix sandbox and immediately re-running a follow-up
			// verb used to resolve the alias to the dying sandbox and
			// wait forever for its pod. Skipping it costs only the
			// warm workspace: the canonical name below is free, so a
			// fresh sandbox is created instead of colliding.
			if terminating(sb) {
				klog.Infof("sandbox %s carries the PR label but is terminating; not reusing it", sb.GetName())
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
		if terminating(sbGet) {
			// Only the canonical name blocks creation below; a dying
			// legacy-named sandbox can simply be ignored.
			if candidate == name {
				if werr := awaitSandboxGone(ctx, kubeClient, namespace, candidate); werr != nil {
					return "", werr
				}
			}
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

// SuspendSandbox scales a single sandbox down to zero replicas.
func SuspendSandbox(ctx context.Context, kubeClient *clients.KubernetesClient, namespace, name string) error {
	if kubeClient == nil || name == "" {
		return nil
	}

	current, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("reading sandbox %s before suspending it: %w", name, err)
	}

	replicas, found, _ := unstructured.NestedInt64(current.Object, "spec", "replicas")
	if found && replicas == 0 {
		return nil
	}

	if err := unstructured.SetNestedField(current.Object, int64(0), "spec", "replicas"); err != nil {
		return fmt.Errorf("setting replicas=0 on sandbox %s: %w", name, err)
	}
	if _, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Update(ctx, current, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("updating sandbox %s to replicas=0: %w", name, err)
	}

	fmt.Printf("Suspended sandbox '%s' (replicas=0)\n", name)
	return nil
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

	lastActivity, idle := idleSince(item, idleTimeout, time.Now())
	if !idle {
		return false, nil
	}
	name := item.GetName()
	if IsCurrentSandbox(ctx, kubeClient, item, namespace) {
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
