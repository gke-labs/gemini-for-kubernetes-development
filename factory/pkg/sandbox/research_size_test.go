package sandbox_test

import (
	"context"
	"testing"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/sandbox"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// A research sandbox nobody sized gets a conversation's size, which
// builds and tests the repository; what the caller chose, by flag or
// SANDBOX_* env, stands.
func TestResearchSandboxSize(t *testing.T) {
	size := func(t *testing.T, disk, ephemeral string) map[string]string {
		t.Helper()
		ns := "u"
		kc := prClients(t, ns)
		name, err := sandbox.EnsureResearchSandbox(context.Background(), kc, ns, "k8s-config-connector", "s1", "", "", "", disk, "", ephemeral, nil, nil, "")
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
	for _, k := range []string{"SANDBOX_CPU_REQUEST", "SANDBOX_CPU_LIMIT", "SANDBOX_MEMORY_REQUEST", "SANDBOX_MEMORY_LIMIT"} {
		t.Setenv(k, "")
	}
	got := size(t, "", "")
	want := map[string]string{"cpu": "4/8", "mem": "12Gi/16Gi", "eph": "10Gi", "pvc": "40Gi"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("default %s = %s, want %s", k, got[k], v)
		}
	}

	t.Setenv("SANDBOX_CPU_REQUEST", "2")
	t.Setenv("SANDBOX_MEMORY_LIMIT", "8Gi")
	got = size(t, "20Gi", "8Gi")
	// A chosen limit below the default request caps it; a chosen request
	// above the default limit raises it.
	if got["cpu"] != "2/8" || got["mem"] != "8Gi/8Gi" || got["eph"] != "8Gi" || got["pvc"] != "20Gi" {
		t.Errorf("chosen sizes not kept: %v", got)
	}
	t.Setenv("SANDBOX_CPU_REQUEST", "10")
	t.Setenv("SANDBOX_MEMORY_LIMIT", "")
	if got = size(t, "", ""); got["cpu"] != "10/10" || got["mem"] != "12Gi/16Gi" {
		t.Errorf("a request above the default limit: %v", got)
	}
}
