package sandbox

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// AnnotationRequiresHuman marks a sandbox whose last run failed in a way
// running it again would repeat, with why: the watch dispatches nothing to
// it, and dashboards show it as needing a person, until someone removes
// the annotation.
const AnnotationRequiresHuman = "factory.gemini.google.com/requires-human"

// RequiresHuman is why the sandbox with annotations needs a person, or "".
func RequiresHuman(annotations map[string]string) string {
	return annotations[AnnotationRequiresHuman]
}

// MarkRequiresHuman sets the sandbox's AnnotationRequiresHuman to reason.
func MarkRequiresHuman(ctx context.Context, kubeClient *clients.KubernetesClient, namespace, sandboxName, reason string) error {
	patch, err := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": map[string]string{AnnotationRequiresHuman: reason}}})
	if err != nil {
		return err
	}
	if _, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Patch(ctx, sandboxName, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("marking sandbox %s as needing a person: %w", sandboxName, err)
	}
	return nil
}
