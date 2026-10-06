package sandbox_test

import (
	"context"
	"testing"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/sandbox"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// A reused sandbox the idle suspender scaled to zero must come back up:
// the caller connects to envd next, before anything else would wake it.
func TestReusedSandboxIsWoken(t *testing.T) {
	ns := "u"
	sb := prSandbox("recipe-open-rl-7", ns, "", "", false)
	_ = unstructured.SetNestedField(sb.Object, int64(0), "spec", "replicas")
	kc := reviewClients(t, ns, sb)

	name, err := sandbox.EnsureRecipeSandbox(context.Background(), kc, ns, "open-rl", 7, "", "", "", "", "", "", "", nil, nil, "")
	if err != nil {
		t.Fatalf("EnsureRecipeSandbox: %v", err)
	}
	got, err := kc.DynamicClient.Resource(k8s.SandboxGVR).Namespace(ns).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if r, _, _ := unstructured.NestedInt64(got.Object, "spec", "replicas"); r != 1 {
		t.Errorf("replicas = %d, want 1", r)
	}
	if got.GetAnnotations()["sandbox.gemini.google.com/unpaused-at"] == "" {
		t.Error("unpaused-at not set; the idle suspender would put it straight back")
	}
}

// A credentials: clone recipe's PR sandbox is its own, which no recipe
// holding the token runs in.
func TestRecipeSandboxName(t *testing.T) {
	if got := sandbox.RecipeSandboxName("open-rl", 7, ""); got != "recipe-open-rl-7" {
		t.Errorf("shared = %s", got)
	}
	if got := sandbox.RecipeSandboxName("open-rl", 7, "review"); got != "review-open-rl-7" {
		t.Errorf("own = %s", got)
	}
}
