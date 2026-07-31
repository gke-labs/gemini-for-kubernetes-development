/*
Copyright 2026.

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

package repowatch

import (
	"context"
	"testing"
	"time"

	sandboxtaskv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/sandboxtask/v1alpha1"
	"github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	sandboxv1alpha1 "sigs.k8s.io/agent-sandbox/api/v1alpha1"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestPauseSandboxIfIdle_UnpausedAt(t *testing.T) {
	g := gomega.NewWithT(t)
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = sandboxv1alpha1.AddToScheme(s)
	_ = sandboxtaskv1alpha1.AddToScheme(s)

	ctx := context.Background()

	// Sandbox created 2 hours ago
	oldSandbox := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "agents.x-k8s.io/v1alpha1",
			"kind":       "Sandbox",
			"metadata": map[string]interface{}{
				"name":              "test-sandbox",
				"namespace":         "default",
				"creationTimestamp": time.Now().Add(-2 * time.Hour).Format(time.RFC3339),
			},
			"spec": map[string]interface{}{"replicas": int64(1)},
		},
	}

	r := &Reconciler{
		Client: clientfake.NewClientBuilder().WithScheme(s).WithObjects(oldSandbox).Build(),
		Scheme: s,
	}

	shutdownDuration := 30 * time.Minute

	// Without unpaused-at annotation, the 2-hour old sandbox should be paused
	paused, err := r.pauseSandboxIfIdle(ctx, oldSandbox, shutdownDuration)
	g.Expect(err).To(gomega.Succeed())
	g.Expect(paused).To(gomega.BeTrue(), "Expected idle sandbox without unpaused-at annotation to be paused")

	// Verify replicas set to 0 in database
	updated := &unstructured.Unstructured{}
	updated.SetGroupVersionKind(schema.GroupVersionKind{Group: "agents.x-k8s.io", Version: "v1alpha1", Kind: "Sandbox"})
	g.Expect(r.Client.Get(ctx, types.NamespacedName{Name: "test-sandbox", Namespace: "default"}, updated)).To(gomega.Succeed())
	replicas, _, _ := unstructured.NestedInt64(updated.Object, "spec", "replicas")
	g.Expect(replicas).To(gomega.Equal(int64(0)))

	// Now unpause the sandbox by setting replicas back to 1 and adding the unpaused-at annotation set to now
	_ = unstructured.SetNestedField(updated.Object, int64(1), "spec", "replicas")
	annotations := map[string]interface{}{
		"sandbox.gemini.google.com/unpaused-at": time.Now().Format(time.RFC3339),
	}
	_ = unstructured.SetNestedField(updated.Object, annotations, "metadata", "annotations")
	g.Expect(r.Client.Update(ctx, updated)).To(gomega.Succeed())

	// Call pauseSandboxIfIdle again on the unpaused sandbox
	paused, err = r.pauseSandboxIfIdle(ctx, updated, shutdownDuration)
	g.Expect(err).To(gomega.Succeed())
	g.Expect(paused).To(gomega.BeFalse(), "Expected unpaused sandbox to stay unpaused for at least shutdownDuration")

	// Verify replicas remain 1
	g.Expect(r.Client.Get(ctx, types.NamespacedName{Name: "test-sandbox", Namespace: "default"}, updated)).To(gomega.Succeed())
	replicas, _, _ = unstructured.NestedInt64(updated.Object, "spec", "replicas")
	g.Expect(replicas).To(gomega.Equal(int64(1)))

	// Simulate that unpaused-at was set 1 hour ago (exceeding 30m shutdownDuration)
	annotations["sandbox.gemini.google.com/unpaused-at"] = time.Now().Add(-1 * time.Hour).Format(time.RFC3339)
	_ = unstructured.SetNestedField(updated.Object, annotations, "metadata", "annotations")
	g.Expect(r.Client.Update(ctx, updated)).To(gomega.Succeed())

	paused, err = r.pauseSandboxIfIdle(ctx, updated, shutdownDuration)
	g.Expect(err).To(gomega.Succeed())
	g.Expect(paused).To(gomega.BeTrue(), "Expected unpaused sandbox to scale down after shutdownDuration has elapsed since unpause")
}
