package sandbox

import (
	"context"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
)

// EnsureTriageSandbox creates (or reuses) the sandbox for triaging an
// issue, named triage-<repo>-<issueNumber>.
func EnsureTriageSandbox(ctx context.Context, kubeClient *clients.KubernetesClient, namespace, repoName string, issueNum int, cloneURL, htmlURL, image, diskSize, storageClass, ephemeralStorage string, secrets []SecretMount, envs []EnvVar, user string) (string, error) {
	name := fmt.Sprintf("triage-%s-%d", repoName, issueNum)
	return ensureTaskSandbox(ctx, kubeClient, namespace, name, "triage", repoName, cloneURL, htmlURL, image, diskSize, storageClass, ephemeralStorage, secrets, envs, user)
}

// EnsureRecipeSandbox creates (or reuses) the sandbox `factory recipe run`
// works in for one issue or PR, named recipe-<repo>-<number>. Not
// triage-…: repo-agent resumes triage sandboxes it finds unfinished.
func EnsureRecipeSandbox(ctx context.Context, kubeClient *clients.KubernetesClient, namespace, repoName string, number int, cloneURL, htmlURL, image, diskSize, storageClass, ephemeralStorage string, secrets []SecretMount, envs []EnvVar, user string) (string, error) {
	name := RecipeSandboxName(repoName, number)
	return ensureTaskSandbox(ctx, kubeClient, namespace, name, "recipe", repoName, cloneURL, htmlURL, image, diskSize, storageClass, ephemeralStorage, secrets, envs, user)
}

// RecipeSandboxName is the sandbox `factory recipe run` uses for issue or
// PR number of repo.
func RecipeSandboxName(repo string, number int) string {
	return fmt.Sprintf("recipe-%s-%d", repo, number)
}

func ensureTaskSandbox(ctx context.Context, kubeClient *clients.KubernetesClient, namespace, name, sandboxType, repoName, cloneURL, htmlURL, image, diskSize, storageClass, ephemeralStorage string, secrets []SecretMount, envs []EnvVar, user string) (string, error) {

	sb, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		prepareReusedSandbox(ctx, kubeClient, namespace, sb, user)
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
				"sandbox.gemini.google.com/type":    sandboxType,
				"factory.gemini.google.com/managed": "true",
				"factory.gemini.google.com/user":    user,
			},
			Annotations: map[string]string{
				"repo":     repoName,
				"cloneURL": cloneURL,
				"htmlURL":  htmlURL,
			},
			Image:                 image,
			Replicas:              1,
			WorkspaceDiskSize:     diskSize,
			WorkspaceStorageClass: storageClass,
			EphemeralStorage:      ephemeralStorage,
			Secrets:               secrets,
			Env:                   envs,
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
