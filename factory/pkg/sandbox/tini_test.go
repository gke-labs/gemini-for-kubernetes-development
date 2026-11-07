package sandbox

import (
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// A sandbox's daemon starts under tini when its image has it (PID 1 reaps
// the zombies builds and tests leave), else as before; a sandbox made
// before that moves to it with its next image, and a command of its own
// is left alone.
func TestDaemonUnderTini(t *testing.T) {
	cmd := DaemonCommand()
	if len(cmd) != 3 || cmd[0] != "sh" || cmd[2] != "if command -v tini >/dev/null 2>&1; then exec tini -- factory daemon; fi; exec factory daemon" {
		t.Errorf("DaemonCommand = %v", cmd)
	}
	sb := func(command []interface{}) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]interface{}{
			"spec": map[string]interface{}{"podTemplate": map[string]interface{}{"spec": map[string]interface{}{
				"containers": []interface{}{map[string]interface{}{"name": "sandbox", "image": "ghcr.io/x/factory-golang:old", "command": command}},
			}}},
		}}
	}
	command := func(s *unstructured.Unstructured) interface{} {
		ctrs, _, _ := unstructured.NestedSlice(s.Object, "spec", "podTemplate", "spec", "containers")
		return ctrs[0].(map[string]interface{})["command"]
	}
	old := sb([]interface{}{"factory", "daemon"})
	if !repinImage(old, "ghcr.io/x/factory-golang:new") || !reflect.DeepEqual(command(old), DaemonCommand()) {
		t.Errorf("an old sandbox's command after repin = %v", command(old))
	}
	own := sb([]interface{}{"sleep", "infinity"})
	repinImage(own, "ghcr.io/x/factory-golang:new")
	if !reflect.DeepEqual(command(own), []interface{}{"sleep", "infinity"}) {
		t.Errorf("a command of its own was replaced: %v", command(own))
	}
}
