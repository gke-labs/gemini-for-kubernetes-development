package sandbox

import (
	"context"
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

var warmNow = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

func warmTestSnapshot(name, repo, image string, created time.Time, ready bool, expires time.Time) *unstructured.Unstructured {
	s := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "snapshot.storage.k8s.io/v1",
		"kind":       "VolumeSnapshot",
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": "ns",
			"labels":    map[string]interface{}{LabelWarmWorkspace: repo},
			"annotations": map[string]interface{}{
				AnnotationWarmImage:   image,
				AnnotationWarmExpires: expires.Format(time.RFC3339),
			},
		},
		"status": map[string]interface{}{"readyToUse": ready, "restoreSize": "40Gi"},
	}}
	s.SetCreationTimestamp(metav1.NewTime(created))
	return s
}

func warmTestClients(objs ...runtime.Object) *clients.KubernetesClient {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{k8s.SandboxGVR: "SandboxList", VolumeSnapshotGVR: "VolumeSnapshotList"}, objs...)
	return &clients.KubernetesClient{DynamicClient: dyn, Clientset: k8sfake.NewSimpleClientset()}
}

// warmTestClientsWithoutTheAPI is a cluster without the snapshot API:
// listing snapshots fails.
func warmTestClientsWithoutTheAPI() *clients.KubernetesClient {
	kc := warmTestClients()
	kc.DynamicClient.(*dynamicfake.FakeDynamicClient).PrependReactor("list", "volumesnapshots", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewNotFound(VolumeSnapshotGVR.GroupResource(), "")
	})
	return kc
}

func TestWarmSnapshotPicksTheNewestUsable(t *testing.T) {
	day := 24 * time.Hour
	later := warmNow.Add(day)
	kc := warmTestClients(
		warmTestSnapshot("old", "k8s-config-connector", "img:1", warmNow.Add(-3*day), true, later),
		warmTestSnapshot("good", "k8s-config-connector", "img:1", warmNow.Add(-2*day), true, later),
		warmTestSnapshot("not-ready", "k8s-config-connector", "img:1", warmNow.Add(-time.Hour), false, later),
		warmTestSnapshot("other-image", "k8s-config-connector", "img:2", warmNow.Add(-time.Hour), true, later),
		warmTestSnapshot("expired", "k8s-config-connector", "img:1", warmNow.Add(-time.Hour), true, warmNow.Add(-time.Minute)),
		warmTestSnapshot("other-repo", "substrate", "img:1", warmNow.Add(-time.Hour), true, later),
	)
	name, size, ok, err := warmSnapshot(context.Background(), kc, "ns", "k8s-config-connector", "img:1", warmNow)
	if err != nil || !ok || name != "good" || size.String() != "40Gi" {
		t.Fatalf("warmSnapshot = %q %s %v %v, want good 40Gi", name, size.String(), ok, err)
	}
	if _, _, ok, _ := warmSnapshot(context.Background(), kc, "ns", "k8s-config-connector", "img:3", warmNow); ok {
		t.Fatal("a snapshot of another image was chosen")
	}
}

func warmTestSandbox(name, repo, image, disk string, labels map[string]string) (*unstructured.Unstructured, string) {
	sb, svc := NewAgentSandbox(AgentSandboxOptions{DevSandboxOptions: DevSandboxOptions{
		Name: name, Namespace: "ns", Image: image, WorkspaceDiskSize: disk, Replicas: 1,
		Labels: labels, Annotations: map[string]string{"repo": repo},
	}})
	return sb, svc.Name
}

func workspaceClaim(t *testing.T, sb *unstructured.Unstructured) map[string]interface{} {
	t.Helper()
	claims, _, _ := unstructured.NestedSlice(sb.Object, "spec", "volumeClaimTemplates")
	if len(claims) != 1 {
		t.Fatalf("claims = %v", claims)
	}
	return claims[0].(map[string]interface{})
}

func TestCreateSandboxRestoresFromTheWarmSnapshot(t *testing.T) {
	kc := warmTestClients(warmTestSnapshot("warm-kcc-1", "k8s-config-connector", "img:1", time.Now().Add(-time.Hour), true, time.Now().Add(time.Hour)))
	sb, _ := warmTestSandbox("fix-k8s-config-connector-1", "k8s-config-connector", "img:1", "10Gi", nil)
	createSandboxForTest(t, kc, sb)
	got, _ := kc.DynamicClient.Resource(k8s.SandboxGVR).Namespace("ns").Get(context.Background(), sb.GetName(), metav1.GetOptions{})
	claim := workspaceClaim(t, got)
	ds, _, _ := unstructured.NestedStringMap(claim, "spec", "dataSource")
	if ds["kind"] != "VolumeSnapshot" || ds["name"] != "warm-kcc-1" || ds["apiGroup"] != "snapshot.storage.k8s.io" {
		t.Fatalf("dataSource = %v", ds)
	}
	if size, _, _ := unstructured.NestedString(claim, "spec", "resources", "requests", "storage"); size != "40Gi" {
		t.Fatalf("storage = %s, want the snapshot's 40Gi", size)
	}
	if got.GetAnnotations()[AnnotationWarmRestored] != "warm-kcc-1" {
		t.Fatalf("annotations = %v", got.GetAnnotations())
	}
}

func TestCreateSandboxKeepsALargerDisk(t *testing.T) {
	kc := warmTestClients(warmTestSnapshot("warm-kcc-1", "k8s-config-connector", "img:1", time.Now().Add(-time.Hour), true, time.Now().Add(time.Hour)))
	sb, _ := warmTestSandbox("fix-k8s-config-connector-1", "k8s-config-connector", "img:1", "100Gi", nil)
	createSandboxForTest(t, kc, sb)
	if size, _, _ := unstructured.NestedString(workspaceClaim(t, sb), "spec", "resources", "requests", "storage"); size != "100Gi" {
		t.Fatalf("storage = %s, want 100Gi", size)
	}
}

func TestCreateSandboxWithoutASnapshot(t *testing.T) {
	for name, kc := range map[string]*clients.KubernetesClient{
		"none":   warmTestClients(),
		"no API": warmTestClientsWithoutTheAPI(),
	} {
		t.Run(name, func(t *testing.T) {
			sb, _ := warmTestSandbox("fix-k8s-config-connector-1", "k8s-config-connector", "img:1", "10Gi", nil)
			createSandboxForTest(t, kc, sb)
			if _, found, _ := unstructured.NestedMap(workspaceClaim(t, sb), "spec", "dataSource"); found {
				t.Fatal("restored without a snapshot")
			}
		})
	}
}

func TestTheWarmSandboxIsNeverRestored(t *testing.T) {
	kc := warmTestClients(warmTestSnapshot("warm-kcc-1", "k8s-config-connector", "img:1", time.Now().Add(-time.Hour), true, time.Now().Add(time.Hour)))
	sb, _ := warmTestSandbox("warm-k8s-config-connector", "k8s-config-connector", "img:1", "10Gi", map[string]string{LabelWarmWorkspace: "k8s-config-connector"})
	createSandboxForTest(t, kc, sb)
	if _, found, _ := unstructured.NestedMap(workspaceClaim(t, sb), "spec", "dataSource"); found {
		t.Fatal("the warm sandbox was restored")
	}
}

func createSandboxForTest(t *testing.T, kc *clients.KubernetesClient, sb *unstructured.Unstructured) {
	t.Helper()
	_, svc := NewAgentSandbox(AgentSandboxOptions{DevSandboxOptions: DevSandboxOptions{Name: sb.GetName(), Namespace: "ns"}})
	if err := createSandbox(context.Background(), kc, "ns", sb, svc); err != nil {
		t.Fatal(err)
	}
}
