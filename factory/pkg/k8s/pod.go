package k8s

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// sandboxPodPoll is how often SandboxPod looks for the sandbox's pod, and
// sandboxPodWait how long it looks before giving up.
var (
	sandboxPodPoll = 3 * time.Second
	sandboxPodWait = 6 * time.Minute
)

// PodStartTimeout begins the error SandboxPod returns when no pod became
// ready in time - a pod still scheduling or scaling up a node, not a
// broken task: the watch's dispatcher tries such a task again later.
const PodStartTimeout = "timed out waiting for sandbox pod"

// SandboxPod is the name of the sandbox's ready pod, once there is one.
// A sandbox the idle suspender scaled to zero is woken first, and stamped
// unpaused-at: without the stamp the suspender, counting from the last
// task, scales it straight back to zero before the pod is ready. A
// missing sandbox is an error at once rather than a wait, and so is a
// pod that has failed with nothing live or pending beside it.
func SandboxPod(ctx context.Context, namespace, sandboxName string) (string, error) {
	kube, err := clients.NewKubernetesClient()
	if err != nil {
		return "", fmt.Errorf("creating k8s client: %w", err)
	}
	return sandboxPod(ctx, kube, namespace, sandboxName)
}

func sandboxPod(ctx context.Context, kube *clients.KubernetesClient, namespace, sandboxName string) (string, error) {
	sb, err := kube.DynamicClient.Resource(SandboxGVR).Namespace(namespace).Get(ctx, sandboxName, v1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("sandbox '%s' does not exist in namespace '%s'", sandboxName, namespace)
	}
	if replicas, found, _ := unstructuredInt(sb.Object, "spec", "replicas"); found && replicas == 0 {
		fmt.Printf("Sandbox %s is currently suspended (replicas=0). Setting replicas to 1 to bring up the pod back...\n", sandboxName)
		patch := fmt.Sprintf(`{"spec":{"replicas":1},"metadata":{"annotations":{"sandbox.gemini.google.com/unpaused-at":%q}}}`, time.Now().UTC().Format(time.RFC3339))
		_, _ = kube.DynamicClient.Resource(SandboxGVR).Namespace(namespace).Patch(ctx, sandboxName, types.MergePatchType, []byte(patch), v1.PatchOptions{})
	}

	deadline := time.Now().Add(sandboxPodWait)
	for {
		pods, err := kube.Clientset.CoreV1().Pods(namespace).List(ctx, v1.ListOptions{LabelSelector: "sandbox=" + sandboxName})
		if err == nil {
			name, failed := readyPod(pods.Items)
			if name != "" {
				return name, nil
			}
			if failed != "" {
				return "", fmt.Errorf("%s: cannot connect to terminated pod", failed)
			}
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("%s %s to become ready", PodStartTimeout, sandboxName)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(sandboxPodPoll):
		}
	}
}

// readyPod is the ready pod among a sandbox's pods, unless one is still
// terminating (a restart in progress, whose replacement is the one to
// use). With none live or pending, failed describes the last pod that
// failed or was evicted.
func readyPod(pods []corev1.Pod) (name, failed string) {
	terminating, live := false, false
	for _, pod := range pods {
		if pod.DeletionTimestamp != nil {
			terminating = true
			continue
		}
		switch {
		case pod.Status.Phase == corev1.PodRunning:
			live = true
			for _, c := range pod.Status.Conditions {
				if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
					name = pod.Name
				}
			}
		case pod.Status.Phase == corev1.PodPending:
			live = true
		case pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded || strings.EqualFold(pod.Status.Reason, "Evicted"):
			reason := pod.Status.Reason
			if reason == "" {
				reason = string(pod.Status.Phase)
			}
			failed = fmt.Sprintf("sandbox pod %s is in %s state (reason: %s)", pod.Name, pod.Status.Phase, reason)
		}
	}
	if terminating {
		name = ""
	}
	if live {
		failed = ""
	}
	return name, failed
}

// unstructuredInt reads an integer field of an unstructured object,
// which JSON decoding may have made an int64 or a float64.
func unstructuredInt(obj map[string]interface{}, fields ...string) (int64, bool, error) {
	var cur interface{} = obj
	for _, f := range fields {
		m, ok := cur.(map[string]interface{})
		if !ok {
			return 0, false, nil
		}
		if cur, ok = m[f]; !ok {
			return 0, false, nil
		}
	}
	switch v := cur.(type) {
	case int64:
		return v, true, nil
	case float64:
		return int64(v), true, nil
	case int:
		return int64(v), true, nil
	}
	return 0, false, fmt.Errorf("%s is not a number", strings.Join(fields, "."))
}
