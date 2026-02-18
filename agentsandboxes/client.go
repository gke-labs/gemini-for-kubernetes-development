/*
Copyright 2026 The Gemini Authors.

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

package agentsandboxes

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	sandboxapi "sigs.k8s.io/agent-sandbox/api/v1alpha1"
)

var (
	sandboxGVR = sandboxapi.GroupVersion.WithResource("sandboxes")
)

// Client is a client for managing agent sandboxes.
type Client struct {
	kube    kubernetes.Interface
	dynamic dynamic.Interface
	ns      string
}

// NewClient creates a new Client using the default kubeconfig.
func NewClient() (*Client, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	configOverrides := &clientcmd.ConfigOverrides{}
	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)

	config, err := kubeConfig.ClientConfig()
	if err != nil {
		return nil, err
	}

	ns, _, err := kubeConfig.Namespace()
	if err != nil {
		ns = "default"
	}

	return NewClientFromConfig(config, ns)
}

// NewClientFromConfig creates a new Client from the given rest.Config.
func NewClientFromConfig(config *rest.Config, namespace string) (*Client, error) {
	kube, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	dyn, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	return &Client{
		kube:    kube,
		dynamic: dyn,
		ns:      namespace,
	}, nil
}

// List returns a list of sandboxes in the client's namespace.
func (c *Client) List(ctx context.Context) ([]*Sandbox, error) {
	list, err := c.dynamic.Resource(sandboxGVR).Namespace(c.ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	var res []*Sandbox
	for _, item := range list.Items {
		s := &sandboxapi.Sandbox{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(item.Object, s); err != nil {
			return nil, err
		}
		res = append(res, &Sandbox{
			Name:      s.Name,
			Namespace: s.Namespace,
			Status:    s.Status,
		})
	}
	return res, nil
}

// New starts a fluent builder for a new sandbox.
func (c *Client) New(name string) *SandboxBuilder {
	return &SandboxBuilder{
		client: c,
		name:   name,
	}
}

// Sandbox represents a sandboxed environment.
type Sandbox struct {
	Name      string
	Namespace string
	Status    sandboxapi.SandboxStatus
}

// SandboxBuilder is a fluent builder for creating a Sandbox.
type SandboxBuilder struct {
	client *Client
	name   string
	image  string
	env    map[string]string
	labels map[string]string
}

// Image sets the container image for the sandbox.
func (b *SandboxBuilder) Image(image string) *SandboxBuilder {
	b.image = image
	return b
}

// Env adds an environment variable to the sandbox.
func (b *SandboxBuilder) Env(name, value string) *SandboxBuilder {
	if b.env == nil {
		b.env = make(map[string]string)
	}
	b.env[name] = value
	return b
}

// Label adds a label to the sandbox.
func (b *SandboxBuilder) Label(name, value string) *SandboxBuilder {
	if b.labels == nil {
		b.labels = make(map[string]string)
	}
	b.labels[name] = value
	return b
}

// Create creates the sandbox in Kubernetes.
func (b *SandboxBuilder) Create(ctx context.Context) (*Sandbox, error) {
	sandbox := &sandboxapi.Sandbox{
		TypeMeta: metav1.TypeMeta{
			APIVersion: sandboxapi.GroupVersion.String(),
			Kind:       "Sandbox",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      b.name,
			Namespace: b.client.ns,
			Labels:    b.labels,
		},
		Spec: sandboxapi.SandboxSpec{
			PodTemplate: sandboxapi.PodTemplate{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "agent",
							Image: b.image,
						},
					},
				},
			},
		},
	}

	for k, v := range b.env {
		sandbox.Spec.PodTemplate.Spec.Containers[0].Env = append(sandbox.Spec.PodTemplate.Spec.Containers[0].Env, corev1.EnvVar{
			Name:  k,
			Value: v,
		})
	}

	uObj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(sandbox)
	if err != nil {
		return nil, err
	}

	u, err := b.client.dynamic.Resource(sandboxGVR).Namespace(b.client.ns).Create(ctx, &unstructured.Unstructured{Object: uObj}, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}

	res := &sandboxapi.Sandbox{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, res); err != nil {
		return nil, err
	}

	return &Sandbox{
		Name:      res.Name,
		Namespace: res.Namespace,
		Status:    res.Status,
	}, nil
}

// Delete deletes the sandbox.
func (c *Client) Delete(ctx context.Context, name string) error {
	return c.dynamic.Resource(sandboxGVR).Namespace(c.ns).Delete(ctx, name, metav1.DeleteOptions{})
}

// Get retrieves a sandbox by name.
func (c *Client) Get(ctx context.Context, name string) (*Sandbox, error) {
	u, err := c.dynamic.Resource(sandboxGVR).Namespace(c.ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	res := &sandboxapi.Sandbox{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, res); err != nil {
		return nil, err
	}

	return &Sandbox{
		Name:      res.Name,
		Namespace: res.Namespace,
		Status:    res.Status,
	}, nil
}

// Top-level functions for convenience

var defaultClient *Client

func init() {
	// We don't initialize defaultClient here because it might fail if not in a k8s environment
	// and we don't want to panic on import.
}

func getDefaultClient() (*Client, error) {
	if defaultClient == nil {
		var err error
		defaultClient, err = NewClient()
		if err != nil {
			return nil, err
		}
	}
	return defaultClient, nil
}

// List is a convenience wrapper for Client.List using a default client.
func List(ctx context.Context) ([]*Sandbox, error) {
	c, err := getDefaultClient()
	if err != nil {
		return nil, err
	}
	return c.List(ctx)
}

// New is a convenience wrapper for Client.New using a default client.
func New(name string) (*SandboxBuilder, error) {
	c, err := getDefaultClient()
	if err != nil {
		return nil, err
	}
	return c.New(name), nil
}
