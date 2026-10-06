package sandbox

import (
	"context"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
)

// EnsureRecipeSandbox creates (or reuses) the sandbox `factory recipe`
// runs a recipe in for a PR: RecipeSandboxName. An issue's recipes run in
// the issue's sandbox (EnsureFixSandbox).
func EnsureRecipeSandbox(ctx context.Context, kubeClient *clients.KubernetesClient, namespace, repoName string, number int, noCredentials bool, cloneURL, htmlURL, image, diskSize, storageClass, ephemeralStorage string, secrets []SecretMount, envs []EnvVar, user string) (string, error) {
	name := RecipeSandboxName(repoName, number, noCredentials)
	return ensureTaskSandbox(ctx, kubeClient, namespace, name, "recipe", repoName, cloneURL, htmlURL, image, diskSize, storageClass, ephemeralStorage, secrets, envs, user)
}

// RecipeSandboxName is the sandbox of a PR's recipes,
// recipe-<repo>-<number>, or with noCredentials, for credentials: clone
// recipes, recipe-<repo>-<number>-nocreds: setup-git leaves the token in
// gh's hosts.yml on the workspace, so a sandbox a recipe holding the token
// ever ran in can no longer promise an agent without one. It is not
// labeled with the PR either, so that `factory pr` (EnsureReviewSandbox)
// never adopts it.
func RecipeSandboxName(repoName string, number int, noCredentials bool) string {
	name := fmt.Sprintf("recipe-%s-%d", repoName, number)
	if noCredentials {
		name += "-nocreds"
	}
	return name
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
