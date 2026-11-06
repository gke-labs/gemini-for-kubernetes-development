package sandbox_test

import (
	"context"
	"strings"
	"testing"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/sandbox"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// A recipe on sandboxes runs in an existing one: its repository is the
// one the item it was made for is in, its account its user label; a
// paused one is woken. Nothing is made.
func TestExistingSandbox(t *testing.T) {
	fakeDynamic := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		k8s.SandboxGVR:            "SandboxList",
		sandbox.VolumeSnapshotGVR: "VolumeSnapshotList",
	})
	kc := &clients.KubernetesClient{DynamicClient: fakeDynamic}
	ctx, ns := context.Background(), "ns"
	mk := func(name, url string, replicas int64) {
		obj := &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "agents.x-k8s.io/v1alpha1", "kind": "Sandbox",
			"metadata": map[string]interface{}{
				"name": name, "namespace": ns,
				"labels":      map[string]interface{}{"factory.gemini.google.com/user": "ada-coder-bot"},
				"annotations": map[string]interface{}{"htmlURL": url},
			},
			"spec": map[string]interface{}{"replicas": replicas},
		}}
		if _, err := fakeDynamic.Resource(k8s.SandboxGVR).Namespace(ns).Create(ctx, obj, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	mk("fix-k8s-config-connector-15081", "https://github.com/GoogleCloudPlatform/k8s-config-connector/issues/15081", 0)
	mk("odd", "", 1)

	es, err := sandbox.GetExistingSandbox(ctx, kc, ns, "fix-k8s-config-connector-15081")
	if err != nil || es.RepoURL != "https://github.com/GoogleCloudPlatform/k8s-config-connector" || es.User != "ada-coder-bot" {
		t.Errorf("GetExistingSandbox = %+v, %v", es, err)
	}
	if _, err := sandbox.GetExistingSandbox(ctx, kc, ns, "missing"); err == nil {
		t.Error("a missing sandbox was found")
	}
	if _, err := sandbox.GetExistingSandbox(ctx, kc, ns, "odd"); err == nil || !strings.Contains(err.Error(), "which repository") {
		t.Errorf("a sandbox without htmlURL: %v", err)
	}

	if err := sandbox.ReuseExistingSandbox(ctx, kc, ns, "fix-k8s-config-connector-15081", "ada-coder-bot", ""); err != nil {
		t.Fatal(err)
	}
	got, _ := fakeDynamic.Resource(k8s.SandboxGVR).Namespace(ns).Get(ctx, "fix-k8s-config-connector-15081", metav1.GetOptions{})
	if r, _, _ := unstructured.NestedInt64(got.Object, "spec", "replicas"); r != 1 {
		t.Errorf("a paused sandbox was not woken: replicas %d", r)
	}
}
