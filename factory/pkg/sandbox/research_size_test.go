package sandbox_test

import (
	"context"
	"testing"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/sandbox"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// A conversation's sandbox is the size its recipe's sandbox: declares, and
// factory's defaults for the rest, which build and test a repository; the
// SANDBOX_* environment the caller's flags set does not size it.
func TestResearchSandboxSize(t *testing.T) {
	size := func(t *testing.T, want sandbox.ResearchSize) map[string]string {
		t.Helper()
		ns := "u"
		kc := prClients(t, ns)
		name, err := sandbox.EnsureResearchSandbox(context.Background(), kc, ns, "k8s-config-connector", "s1", "", "", "", "", want, nil, nil, "")
		if err != nil {
			t.Fatalf("EnsureResearchSandbox: %v", err)
		}
		sb, err := kc.DynamicClient.Resource(k8s.SandboxGVR).Namespace(ns).Get(context.Background(), name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
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

	got := size(t, sandbox.ResearchSize{})
	if got["cpu"] != "4/8" || got["mem"] != "12Gi/16Gi" || got["eph"] != "10Gi" || got["pvc"] != "40Gi" {
		t.Errorf("no sandbox: in the recipe = %v, want factory's defaults", got)
	}

	got = size(t, sandbox.ResearchSize{Disk: "100Gi", CPURequest: "8", CPULimit: "16", MemoryRequest: "32Gi", MemoryLimit: "48Gi", EphemeralStorage: "8Gi"})
	if got["cpu"] != "8/16" || got["mem"] != "32Gi/48Gi" || got["eph"] != "8Gi" || got["pvc"] != "100Gi" {
		t.Errorf("the recipe's sandbox: = %v", got)
	}

	// A recipe that sets one of a pair keeps the pair valid.
	got = size(t, sandbox.ResearchSize{CPULimit: "2", MemoryRequest: "20Gi"})
	if got["cpu"] != "2/2" || got["mem"] != "20Gi/20Gi" || got["pvc"] != "40Gi" {
		t.Errorf("half pairs = %v", got)
	}
}
