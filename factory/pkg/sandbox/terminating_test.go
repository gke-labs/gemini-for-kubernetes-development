package sandbox_test

import (
	"context"
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/sandbox"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

// prSandbox builds a sandbox carrying a PR alias label, optionally on
// its way out.
func prSandbox(name, ns, htmlURL string, prNum string, dying bool) *unstructured.Unstructured {
	meta := map[string]interface{}{
		"name":      name,
		"namespace": ns,
		"labels": map[string]interface{}{
			"factory.gemini.google.com/pr":      prNum,
			"factory.gemini.google.com/managed": "true",
		},
		// sandboxBelongsToRepo reads these two plain keys.
		"annotations": map[string]interface{}{
			"repo":    "open-rl",
			"htmlURL": htmlURL,
		},
	}
	if dying {
		meta["deletionTimestamp"] = time.Now().UTC().Format(time.RFC3339)
		meta["finalizers"] = []interface{}{"agents.x-k8s.io/cleanup"}
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "agents.x-k8s.io/v1alpha1",
		"kind":       "Sandbox",
		"metadata":   meta,
		"spec":       map[string]interface{}{"replicas": int64(1)},
	}}
}

// Objects are seeded with Create rather than handed to the constructor:
// an empty scheme does not register unstructured types, and the
// resulting client lists nothing.
func reviewClients(t *testing.T, ns string, objs ...*unstructured.Unstructured) *clients.KubernetesClient {
	t.Helper()
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{k8s.SandboxGVR: "SandboxList"})
	for _, o := range objs {
		if _, err := dyn.Resource(k8s.SandboxGVR).Namespace(ns).
			Create(context.Background(), o, metav1.CreateOptions{}); err != nil {
			t.Fatalf("seeding %s: %v", o.GetName(), err)
		}
	}
	return &clients.KubernetesClient{DynamicClient: dyn, Clientset: k8sfake.NewSimpleClientset()}
}

func ensureReview(t *testing.T, kc *clients.KubernetesClient, ns string) (string, error) {
	t.Helper()
	return sandbox.EnsureReviewSandbox(context.Background(), kc, ns, 270,
		"chore(deps): batch open dependabot PRs",
		"https://github.com/gke-labs/open-rl/pull/270",
		"https://github.com/gke-labs/open-rl/pull/270.diff",
		"https://github.com/gke-labs/open-rl.git",
		"img:latest", "10Gi", "10Gi", nil, nil, "barney-s")
}

// A PR's fix sandbox carries the PR label. Deleting it and immediately
// re-running a follow-up verb used to resolve the alias to the dying
// sandbox, connect to it, and wait forever for a pod that was on its
// way out.
func TestTerminatingAliasIsNotReused(t *testing.T) {
	ns := "barney-s"
	dying := prSandbox("fix-open-rl-269", ns, "https://github.com/gke-labs/open-rl/pull/270", "270", true)
	kc := reviewClients(t, ns, dying)

	name, err := ensureReview(t, kc, ns)
	if err != nil {
		t.Fatalf("EnsureReviewSandbox: %v", err)
	}
	if name == "fix-open-rl-269" {
		t.Fatalf("reused the terminating sandbox %q; the caller would wait forever for its pod", name)
	}
	if want := sandbox.ReviewSandboxName("open-rl", 270); name != want {
		t.Errorf("name = %q, want the canonical %q", name, want)
	}
	// And it must actually have been created, not merely named.
	if _, err := kc.DynamicClient.Resource(k8s.SandboxGVR).Namespace(ns).
		Get(context.Background(), name, metav1.GetOptions{}); err != nil {
		t.Errorf("fresh sandbox %q was not created: %v", name, err)
	}
}

// The reuse it replaces must still work: a live aliased sandbox is the
// PR's warm workspace and skipping it would re-clone every time.
func TestLiveAliasIsStillReused(t *testing.T) {
	ns := "barney-s"
	live := prSandbox("fix-open-rl-269", ns, "https://github.com/gke-labs/open-rl/pull/270", "270", false)
	kc := reviewClients(t, ns, live)

	name, err := ensureReview(t, kc, ns)
	if err != nil {
		t.Fatalf("EnsureReviewSandbox: %v", err)
	}
	if name != "fix-open-rl-269" {
		t.Errorf("name = %q, want the aliased sandbox to be reused", name)
	}
}
