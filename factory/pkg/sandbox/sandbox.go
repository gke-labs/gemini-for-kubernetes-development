package sandbox

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/klog/v2"
)

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

func EnsureReviewSandbox(ctx context.Context, kubeClient *clients.KubernetesClient, namespace string, prNum int, prTitle, prHTMLURL, prDiffURL, prCloneURL, image, diskSize, ephemeralStorage string, secrets []SecretMount, envs []EnvVar, user string) (string, error) {
	listOpts := metav1.ListOptions{
		LabelSelector: fmt.Sprintf("factory.gemini.google.com/pr=%d", prNum),
	}
	sbs, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).List(ctx, listOpts)
	if err == nil && len(sbs.Items) > 0 {
		sb := sbs.Items[0]
		labels := sb.GetLabels()
		if labels == nil {
			labels = make(map[string]string)
		}
		if labels["factory.gemini.google.com/user"] != user && user != "" {
			labels["factory.gemini.google.com/user"] = user
			sb.SetLabels(labels)
			_, err = kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Update(ctx, &sb, metav1.UpdateOptions{})
			if err != nil {
				klog.Warningf("Failed to update sandbox labels with user '%s': %v", user, err)
			}
		}
		return sb.GetName(), nil
	}

	name := fmt.Sprintf("factory-pr-%d", prNum)

	sbGet, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		labels := sbGet.GetLabels()
		if labels == nil {
			labels = make(map[string]string)
		}
		if labels["factory.gemini.google.com/user"] != user && user != "" {
			labels["factory.gemini.google.com/user"] = user
			sbGet.SetLabels(labels)
			_, err = kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Update(ctx, sbGet, metav1.UpdateOptions{})
			if err != nil {
				klog.Warningf("Failed to update sandbox labels with user '%s': %v", user, err)
			}
		}
		return name, nil
	}
	if !strings.Contains(err.Error(), "not found") {
		return "", fmt.Errorf("checking sandbox existence: %w", err)
	}

	parts := strings.Split(strings.TrimSuffix(prCloneURL, ".git"), "/")
	repo := parts[len(parts)-1]

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

	if taskType != "" && taskState != "Completed" && taskState != "Failed" {
		replicas, found, _ := unstructured.NestedInt64(unstructObj.Object, "spec", "replicas")
		if found && replicas == 0 {
			_ = unstructured.SetNestedField(unstructObj.Object, int64(1), "spec", "replicas")
		}
	}

	annotations := unstructObj.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
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

func SuspendIdleSandboxes(ctx context.Context, kubeClient *clients.KubernetesClient, namespace string, idleTimeout time.Duration, dryRun bool) (int, error) {
	if kubeClient == nil || idleTimeout <= 0 {
		return 0, nil
	}

	list, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return 0, fmt.Errorf("listing sandboxes for idle suspension check: %w", err)
	}

	now := time.Now()
	suspendedCount := 0

	for _, item := range list.Items {
		name := item.GetName()
		replicas, found, err := unstructured.NestedInt64(item.Object, "spec", "replicas")
		if err == nil && found && replicas == 0 {
			continue // Already suspended
		}

		// Determine last activity time (latest of creation time, completion-time, last-task-time)
		lastActivity := item.GetCreationTimestamp().Time
		if annotations := item.GetAnnotations(); annotations != nil {
			if state := annotations["sandbox.gemini.google.com/last-task-state"]; state != "" && !strings.EqualFold(state, "Completed") && !strings.EqualFold(state, "Failed") {
				// There is an active task running right now (e.g. Running), do not suspend
				continue
			}
			if tsStr, ok := annotations["sandbox.gemini.google.com/completion-time"]; ok {
				if ts, err := time.Parse(time.RFC3339, tsStr); err == nil && ts.After(lastActivity) {
					lastActivity = ts
				}
			}
			if tsStr, ok := annotations["sandbox.gemini.google.com/last-task-time"]; ok {
				if ts, err := time.Parse(time.RFC3339, tsStr); err == nil && ts.After(lastActivity) {
					lastActivity = ts
				}
			}
		}

		if now.Sub(lastActivity) > idleTimeout {
			klog.Infof("Sandbox '%s' in namespace '%s' has not run any task for %v (last activity: %v). Suspending (replicas=0)...", name, namespace, idleTimeout, lastActivity)
			if dryRun {
				fmt.Printf("[DRYRUN] Would suspend idle sandbox '%s' (replicas=0)\n", name)
				suspendedCount++
				continue
			}

			if err := unstructured.SetNestedField(item.Object, int64(0), "spec", "replicas"); err != nil {
				klog.Errorf("Failed to set replicas=0 on sandbox '%s': %v", name, err)
				continue
			}
			_, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Update(ctx, &item, metav1.UpdateOptions{})
			if err != nil {
				klog.Errorf("Failed to update sandbox '%s' to replicas=0: %v", name, err)
			} else {
				fmt.Printf("Suspended idle sandbox '%s' (replicas=0)\n", name)
				suspendedCount++
			}
		}
	}

	return suspendedCount, nil
}
