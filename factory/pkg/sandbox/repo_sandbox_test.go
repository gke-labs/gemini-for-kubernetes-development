package sandbox_test

import (
	"context"
	"testing"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/sandbox"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// A repository sandbox is the size its recipe's sandbox: declares, all of it;
// the SANDBOX_* environment the caller's flags set does not size it. Its
// labels and annotations are the generic ones, whatever its prefix.
func TestRepoSandbox(t *testing.T) {
	ns := "u"
	kc := prClients(t, ns)
	ensure := func(t *testing.T, s sandbox.RepoSandbox) *unstructured.Unstructured {
		t.Helper()
		name, err := sandbox.EnsureRepoSandbox(context.Background(), kc, ns, s, "k8s-config-connector", "clone", "html", "", "", nil, nil, "barney-s")
		if err != nil {
			t.Fatalf("EnsureRepoSandbox: %v", err)
		}
		sb, err := kc.DynamicClient.Resource(k8s.SandboxGVR).Namespace(ns).Get(context.Background(), name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return sb
	}
	size := func(sb *unstructured.Unstructured) map[string]string {
		containers, _, _ := unstructured.NestedSlice(sb.Object, "spec", "podTemplate", "spec", "containers")
		res := containers[0].(map[string]interface{})["resources"].(map[string]interface{})
		get := func(kind, name string) string {
			v, _, _ := unstructured.NestedFieldNoCopy(res, kind, name)
			s, _ := v.(string)
			return s
		}
		claims, _, _ := unstructured.NestedSlice(sb.Object, "spec", "volumeClaimTemplates")
		pvc, _, _ := unstructured.NestedString(claims[0].(map[string]interface{}), "spec", "resources", "requests", "storage")
		return map[string]string{
			"cpu": get("requests", "cpu") + "/" + get("limits", "cpu"),
			"mem": get("requests", "memory") + "/" + get("limits", "memory"),
			"eph": get("limits", "ephemeral-storage"),
			"pvc": pvc,
		}
	}
	// What a caller's --cpu-request and the like would set: ignored.
	t.Setenv("SANDBOX_CPU_REQUEST", "500m")
	t.Setenv("SANDBOX_MEMORY_LIMIT", "2Gi")

	big := sandbox.Size{Disk: "100Gi", CPURequest: "8", CPULimit: "16", MemoryRequest: "32Gi", MemoryLimit: "48Gi", EphemeralStorage: "8Gi"}
	sb := ensure(t, sandbox.RepoSandbox{Prefix: "rsch", Key: "s1", Size: big})
	if got := size(sb); got["cpu"] != "8/16" || got["mem"] != "32Gi/48Gi" || got["eph"] != "8Gi" || got["pvc"] != "100Gi" {
		t.Errorf("the recipe's sandbox: = %v", got)
	}
	if sb.GetName() != sandbox.RepoSandboxName("rsch", "k8s-config-connector", "s1") {
		t.Errorf("name = %s", sb.GetName())
	}
	labels, annotations := sb.GetLabels(), sb.GetAnnotations()
	if labels[sandbox.LabelType] != "rsch" || labels[sandbox.LabelKey] != sandbox.ShortID("s1") || annotations[sandbox.AnnotationKeyID] != "s1" {
		t.Errorf("labels %v, annotations %v", labels, annotations)
	}
	if sa, _, _ := unstructured.NestedString(sb.Object, "spec", "podTemplate", "spec", "serviceAccountName"); sa != sandbox.DeployerServiceAccount {
		t.Errorf("service account = %q", sa)
	}

	for name, s := range map[string]sandbox.RepoSandbox{
		"no prefix": {Key: "k"},
		"no key":    {Prefix: "rsch"},
	} {
		if _, err := sandbox.EnsureRepoSandbox(context.Background(), kc, ns, s, "r", "", "", "", "", nil, nil, ""); err == nil {
			t.Errorf("%s: made", name)
		}
	}
}
