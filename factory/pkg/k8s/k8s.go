package k8s

import (
	"context"
	"fmt"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

var (
	SandboxGVR = schema.GroupVersionResource{
		Group:    "agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "sandboxes",
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

func (m *Manager) ListSandboxes(ctx context.Context, namespace string) (*unstructured.UnstructuredList, error) {
	return m.Client.Resource(SandboxGVR).Namespace(namespace).List(ctx, v1.ListOptions{})
}

func (m *Manager) GetSandbox(ctx context.Context, namespace, name string) (*unstructured.Unstructured, error) {
	return m.Client.Resource(SandboxGVR).Namespace(namespace).Get(ctx, name, v1.GetOptions{})
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
