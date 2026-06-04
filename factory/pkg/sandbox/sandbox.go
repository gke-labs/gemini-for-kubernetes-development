package sandbox

import (
	"context"
	"fmt"
	"strings"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func EnsureFixSandbox(ctx context.Context, kubeClient *clients.KubernetesClient, namespace, repoName, taskID, cloneURL, taskTitle, image, diskSize, ephemeralStorage string, secrets []SecretMount, envs []EnvVar) (string, error) {
	name := fmt.Sprintf("fix-%s-%s", repoName, taskID)

	_, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err == nil {
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
			},
			Annotations: map[string]string{
				"repo":     repoName,
				"cloneURL": cloneURL,
			},
			Image:             image,
			Replicas:          1,
			WorkspaceDiskSize: diskSize,
			EphemeralStorage:  ephemeralStorage,
			Secrets:           secrets,
			Env:               envs,
		},
	}

	sb, svc := NewAgentSandbox(opt)

	_, err = kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Create(ctx, sb, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("creating sandbox CR: %w", err)
	}

	_, err = kubeClient.Clientset.CoreV1().Services(namespace).Create(ctx, svc, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("creating sandbox service: %w", err)
	}

	return name, nil
}

func EnsureAgentSandbox(ctx context.Context, kubeClient *clients.KubernetesClient, namespace, repoName, taskID, cloneURL, taskTitle, image, diskSize, ephemeralStorage string, secrets []SecretMount, envs []EnvVar) (string, error) {
	name := fmt.Sprintf("agent-%s-%s", repoName, taskID)

	_, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err == nil {
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
				"sandbox.gemini.google.com/type":    "agent",
				"factory.gemini.google.com/managed": "true",
			},
			Annotations: map[string]string{
				"repo":     repoName,
				"cloneURL": cloneURL,
			},
			Image:             image,
			Replicas:          1,
			WorkspaceDiskSize: diskSize,
			EphemeralStorage:  ephemeralStorage,
			Secrets:           secrets,
			Env:               envs,
		},
	}

	sb, svc := NewAgentSandbox(opt)

	_, err = kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Create(ctx, sb, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("creating sandbox CR: %w", err)
	}

	_, err = kubeClient.Clientset.CoreV1().Services(namespace).Create(ctx, svc, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("creating sandbox service: %w", err)
	}

	return name, nil
}

func AliasSandboxToPR(ctx context.Context, kubeClient *clients.KubernetesClient, namespace, sandboxName string, prNum int) error {
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
	unstructObj.SetAnnotations(annotations)

	_, err = kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Update(ctx, unstructObj, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("updating sandbox %s with PR alias: %w", sandboxName, err)
	}
	return nil
}

func EnsureReviewSandbox(ctx context.Context, kubeClient *clients.KubernetesClient, namespace string, prNum int, prTitle, prHTMLURL, prDiffURL, prCloneURL, image, diskSize, ephemeralStorage string, secrets []SecretMount, envs []EnvVar) (string, error) {
	listOpts := metav1.ListOptions{
		LabelSelector: fmt.Sprintf("factory.gemini.google.com/pr=%d", prNum),
	}
	sbs, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).List(ctx, listOpts)
	if err == nil && len(sbs.Items) > 0 {
		return sbs.Items[0].GetName(), nil
	}

	name := fmt.Sprintf("factory-pr-%d", prNum)

	_, err = kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err == nil {
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
	unstructObj, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Get(ctx, sandboxName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("getting sandbox %s: %w", sandboxName, err)
	}

	annotations := unstructObj.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}

	if taskType != "" {
		annotations["sandbox.gemini.google.com/last-task-type"] = taskType
		annotations["sandbox.gemini.google.com/last-task-state"] = taskState
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
