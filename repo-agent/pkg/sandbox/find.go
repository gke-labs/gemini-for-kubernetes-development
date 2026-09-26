/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package sandbox

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/clients"
)

// RepoSandboxBinary is the legacy in-sandbox agent path; the terminal
// handler probes for it before falling back to `factory sshd`.
const RepoSandboxBinary = "/opt/repo-agent/repo-sandbox"

// FindSandboxPodInNamespace finds the pod for the given sandbox name in the specified namespace.
// If namespace is empty, it uses the current namespace from kube config.
// If the pod is not found, it returns (nil, nil).
func FindSandboxPodInNamespace(ctx context.Context, sandboxName, namespace string) (*types.NamespacedName, error) {
	pod, err := findSandboxPod(ctx, sandboxName, namespace)
	if err != nil || pod == nil {
		return nil, err
	}
	return &types.NamespacedName{
		Name:      pod.Name,
		Namespace: pod.Namespace,
	}, nil
}

// FindSandboxPodIP returns the pod IP for a sandbox, or "" if there is no
// pod or it has not been assigned one yet.
//
// Callers that dial a sandbox port directly need this rather than a pod
// name: the acpd conversation server is not on the <sandbox>-lb Service,
// so there is no stable name to resolve. The IP is only good while the
// pod lives, which is the same lifetime as the thing being dialled.
func FindSandboxPodIP(ctx context.Context, sandboxName, namespace string) (string, error) {
	pod, err := findSandboxPod(ctx, sandboxName, namespace)
	if err != nil || pod == nil {
		return "", err
	}
	if pod.Status.Phase != corev1.PodRunning {
		// A pod IP on a pending or terminating pod is either absent or
		// about to stop answering; report it as not there yet rather
		// than hand back an address that fails on connect.
		return "", nil
	}
	return pod.Status.PodIP, nil
}

// findSandboxPod returns the sandbox's pod, preferring a running one, or
// (nil, nil) when the sandbox has no pods.
func findSandboxPod(ctx context.Context, sandboxName, namespace string) (*corev1.Pod, error) {
	kube, err := clients.NewKubernetesClient()
	if err != nil {
		return nil, err
	}

	clientset := kube.Clientset
	if namespace == "" {
		namespace = kube.CurrentNamespace
	}

	// The sandbox's pods carry the label sandbox=<name>
	labelSelector := fmt.Sprintf("sandbox=%s", sandboxName)
	pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list pods with selector %q: %w", labelSelector, err)
	}

	if len(pods.Items) == 0 {
		return nil, nil
	}

	// Pick the first running pod, or just the first one if none are running yet (though exec will fail)
	pod := &pods.Items[0]

	for i := range pods.Items {
		if pods.Items[i].Status.Phase == corev1.PodRunning {
			pod = &pods.Items[i]
			break
		}
	}
	return pod, nil
}
