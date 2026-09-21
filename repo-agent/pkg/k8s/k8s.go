package k8s

import (
	"context"
	"fmt"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/clients"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

const (
	OAuthPATKey  = "oauth_pat"
	ManualPATKey = "manual_pat"
)

var (
	SandboxGVR = schema.GroupVersionResource{
		Group:    "agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "sandboxes",
	}
	OverseerGVR = schema.GroupVersionResource{
		Group:    "overseer.gemini.google.com",
		Version:  "v1alpha1",
		Resource: "overseers",
	}
)

type Manager struct {
	Client     dynamic.Interface
	Clientset  kubernetes.Interface
	KubeClient *clients.KubernetesClient
}

func NewManager(kube *clients.KubernetesClient) *Manager {
	return &Manager{Client: kube.DynamicClient, Clientset: kube.Clientset, KubeClient: kube}
}

func (m *Manager) DeleteSandbox(ctx context.Context, namespace, name string) error {
	err := m.Client.Resource(SandboxGVR).Namespace(namespace).Delete(ctx, name, v1.DeleteOptions{})
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("deleting sandbox %s: %w", name, err)
	}
	svcName := name + "-lb"
	err = m.Clientset.CoreV1().Services(namespace).Delete(ctx, svcName, v1.DeleteOptions{})
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("deleting service %s: %w", svcName, err)
	}
	return nil
}

func (m *Manager) ListOverseers(ctx context.Context) (*unstructured.UnstructuredList, error) {
	return m.Client.Resource(OverseerGVR).List(ctx, v1.ListOptions{})
}

func (m *Manager) GetOverseer(ctx context.Context, name string) (*unstructured.Unstructured, error) {
	return m.Client.Resource(OverseerGVR).Get(ctx, name, v1.GetOptions{})
}

func (m *Manager) ListSandboxes(ctx context.Context, namespace string, labelSelector string) (*unstructured.UnstructuredList, error) {
	return m.Client.Resource(SandboxGVR).Namespace(namespace).List(ctx, v1.ListOptions{
		LabelSelector: labelSelector,
	})
}

func (m *Manager) UpdateSecret(ctx context.Context, namespace, name string, data map[string][]byte, annotations map[string]string) error {
	secret, err := m.Clientset.CoreV1().Secrets(namespace).Get(ctx, name, v1.GetOptions{})
	if errors.IsNotFound(err) {
		secret = &corev1.Secret{
			ObjectMeta: v1.ObjectMeta{
				Name:        name,
				Namespace:   namespace,
				Annotations: annotations,
			},
			Data: data,
		}
		_, err = m.Clientset.CoreV1().Secrets(namespace).Create(ctx, secret, v1.CreateOptions{})
		return err
	} else if err != nil {
		return err
	}

	if secret.Data == nil {
		secret.Data = make(map[string][]byte)
	}
	for k, v := range data {
		if v == nil {
			delete(secret.Data, k)
		} else {
			secret.Data[k] = v
		}
	}

	if secret.Annotations == nil {
		secret.Annotations = make(map[string]string)
	}
	for k, v := range annotations {
		secret.Annotations[k] = v
	}

	_, err = m.Clientset.CoreV1().Secrets(namespace).Update(ctx, secret, v1.UpdateOptions{})
	return err
}

func (m *Manager) UpdateSandboxAnnotation(ctx context.Context, namespace, sandboxName, key, value string) error {
	sandbox, err := m.Client.Resource(SandboxGVR).Namespace(namespace).Get(ctx, sandboxName, v1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get sandbox %s: %w", sandboxName, err)
	}

	if sandbox.GetAnnotations() == nil {
		sandbox.SetAnnotations(make(map[string]string))
	}
	annotations := sandbox.GetAnnotations()
	annotations[key] = value
	sandbox.SetAnnotations(annotations)

	_, err = m.Client.Resource(SandboxGVR).Namespace(namespace).Update(ctx, sandbox, v1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update sandbox annotation: %w", err)
	}

	return nil
}

// UpdateSandboxLabel mirrors UpdateSandboxAnnotation for labels (e.g.
// healing the factory PR alias when a fix child died before stamping it).
func (m *Manager) UpdateSandboxLabel(ctx context.Context, namespace, sandboxName, key, value string) error {
	sandbox, err := m.Client.Resource(SandboxGVR).Namespace(namespace).Get(ctx, sandboxName, v1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get sandbox %s: %w", sandboxName, err)
	}
	labels := sandbox.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels[key] = value
	sandbox.SetLabels(labels)
	if _, err := m.Client.Resource(SandboxGVR).Namespace(namespace).Update(ctx, sandbox, v1.UpdateOptions{}); err != nil {
		return fmt.Errorf("failed to update sandbox label: %w", err)
	}
	return nil
}

func (m *Manager) ScaledownSandboxByName(ctx context.Context, namespace, name string) error {
	log := klog.FromContext(ctx)
	log.Info("Scaling down sandbox by name", "name", name)

	_, err := m.Client.Resource(SandboxGVR).Namespace(namespace).Get(ctx, name, v1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to get sandbox %s: %w", name, err)
	}

	sandbox := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "agents.x-k8s.io/v1alpha1",
			"kind":       "Sandbox",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
			},
			"spec": map[string]interface{}{
				"replicas": int64(0),
			},
		},
	}

	_, err = m.Client.Resource(SandboxGVR).Namespace(namespace).Apply(ctx, name,
		sandbox, v1.ApplyOptions{FieldManager: "review-ui", Force: true})
	if err != nil {
		return fmt.Errorf("failed to scaledown sandbox: %w", err)
	}
	return nil
}

func (m *Manager) ScaleupSandboxByName(ctx context.Context, namespace, name string) error {
	log := klog.FromContext(ctx)
	log.Info("Scaling up sandbox by name", "name", name)

	_, err := m.Client.Resource(SandboxGVR).Namespace(namespace).Get(ctx, name, v1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to get sandbox %s: %w", name, err)
	}

	metadata := map[string]interface{}{
		"name":      name,
		"namespace": namespace,
		"annotations": map[string]interface{}{
			"sandbox.gemini.google.com/unpaused-at": time.Now().Format(time.RFC3339),
		},
	}

	sandbox := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "agents.x-k8s.io/v1alpha1",
			"kind":       "Sandbox",
			"metadata":   metadata,
			"spec": map[string]interface{}{
				"replicas": int64(1),
			},
		},
	}

	_, err = m.Client.Resource(SandboxGVR).Namespace(namespace).Apply(ctx, name,
		sandbox, v1.ApplyOptions{FieldManager: "review-ui", Force: true})
	if err != nil {
		return fmt.Errorf("failed to scale up sandbox: %w", err)
	}
	return nil
}
