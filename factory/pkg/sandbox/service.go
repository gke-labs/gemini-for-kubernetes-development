package sandbox

import (
	"context"
	"fmt"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// createSandbox creates the sandbox and its "<name>-lb" Service, owned by
// the sandbox so that however the sandbox is deleted — `kubectl delete`
// included — the Service goes with it.
//
// A Service left by a sandbox deleted before Services were owned is
// adopted rather than refused: its name and selector derive from the
// sandbox's name, so it is the one this would have made.
func createSandbox(ctx context.Context, kubeClient *clients.KubernetesClient, namespace string, sbObj *unstructured.Unstructured, svc *corev1.Service) error {
	created, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Create(ctx, sbObj, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("creating sandbox CR: %w", err)
	}
	owner := []metav1.OwnerReference{*metav1.NewControllerRef(created, created.GroupVersionKind())}
	svc.OwnerReferences = owner
	services := kubeClient.Clientset.CoreV1().Services(namespace)
	_, err = services.Create(ctx, svc, metav1.CreateOptions{})
	if !apierrors.IsAlreadyExists(err) {
		if err != nil {
			return fmt.Errorf("creating sandbox service: %w", err)
		}
		return nil
	}
	existing, err := services.Get(ctx, svc.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("adopting sandbox service: %w", err)
	}
	existing.OwnerReferences = owner
	if _, err := services.Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("adopting sandbox service: %w", err)
	}
	return nil
}
