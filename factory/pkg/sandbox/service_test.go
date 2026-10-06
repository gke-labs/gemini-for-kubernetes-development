package sandbox

import (
	"context"
	"testing"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

func serviceTestSandbox(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "agents.x-k8s.io/v1alpha1",
		"kind":       "Sandbox",
		"metadata":   map[string]interface{}{"name": name, "namespace": "ns", "uid": "sb-uid"},
	}}
}

func serviceTestClients(svcs ...*corev1.Service) *clients.KubernetesClient {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{k8s.SandboxGVR: "SandboxList"})
	cs := k8sfake.NewSimpleClientset()
	for _, s := range svcs {
		_, _ = cs.CoreV1().Services(s.Namespace).Create(context.Background(), s, metav1.CreateOptions{})
	}
	return &clients.KubernetesClient{DynamicClient: dyn, Clientset: cs}
}

func checkOwned(t *testing.T, kc *clients.KubernetesClient, name string) {
	t.Helper()
	got, err := kc.Clientset.CoreV1().Services("ns").Get(context.Background(), name+"-lb", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("service: %v", err)
	}
	refs := got.OwnerReferences
	if len(refs) != 1 || refs[0].Kind != "Sandbox" || refs[0].Name != name || refs[0].UID != "sb-uid" {
		t.Fatalf("owner references = %+v, want the sandbox %s", refs, name)
	}
}

func TestCreateSandboxOwnsItsService(t *testing.T) {
	kc := serviceTestClients()
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "sb-lb", Namespace: "ns"}}
	if err := createSandbox(context.Background(), kc, "ns", serviceTestSandbox("sb"), svc); err != nil {
		t.Fatal(err)
	}
	checkOwned(t, kc, "sb")
}

// A Service left behind by a sandbox deleted before Services were owned
// is adopted, not a reason to fail the create.
func TestCreateSandboxAdoptsALeftoverService(t *testing.T) {
	kc := serviceTestClients(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "sb-lb", Namespace: "ns"}})
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "sb-lb", Namespace: "ns"}}
	if err := createSandbox(context.Background(), kc, "ns", serviceTestSandbox("sb"), svc); err != nil {
		t.Fatal(err)
	}
	checkOwned(t, kc, "sb")
}
