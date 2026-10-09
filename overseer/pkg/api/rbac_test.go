package api

import (
	"bytes"
	"os"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"
)

// clusterRoles reads the ClusterRoles in a multi-document manifest.
func clusterRoles(t *testing.T, path string) map[string]rbacv1.ClusterRole {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	roles := map[string]rbacv1.ClusterRole{}
	for _, doc := range bytes.Split(b, []byte("\n---")) {
		var r rbacv1.ClusterRole
		if err := yaml.Unmarshal(doc, &r); err != nil {
			t.Fatal(err)
		}
		if r.Kind == "ClusterRole" {
			roles[r.Name] = r
		}
	}
	return roles
}

func allows(rules []rbacv1.PolicyRule, group, resource, verb string) bool {
	has := func(list []string, v string) bool {
		for _, x := range list {
			if x == v || x == "*" {
				return true
			}
		}
		return false
	}
	for _, r := range rules {
		if has(r.APIGroups, group) && has(r.Resources, resource) && has(r.Verbs, verb) {
			return true
		}
	}
	return false
}

// TestControllerHoldsWhatItGrants: the controller binds each overseer's
// service accounts to the ClusterRoles overseer and overseer-sandbox, and
// Kubernetes refuses a binding that grants what the binder does not hold.
// A rule added to those roles alone stops every Overseer reconciling.
func TestControllerHoldsWhatItGrants(t *testing.T) {
	controller, ok := clusterRoles(t, "../../k8s/overseer-controller.yaml")["overseer-controller"]
	if !ok {
		t.Fatal("no ClusterRole overseer-controller")
	}
	granted := clusterRoles(t, "../../k8s/overseer-rbac.yaml")
	for name, r := range clusterRoles(t, "../../k8s/sandbox-rbac.yaml") {
		granted[name] = r
	}
	for _, name := range []string{"overseer", "overseer-sandbox"} {
		role, ok := granted[name]
		if !ok {
			t.Fatalf("no ClusterRole %s", name)
		}
		for _, rule := range role.Rules {
			for _, g := range rule.APIGroups {
				for _, res := range rule.Resources {
					for _, v := range rule.Verbs {
						if !allows(controller.Rules, g, res, v) {
							t.Errorf("ClusterRole %s grants %s %q/%s, which overseer-controller does not hold", name, v, g, res)
						}
					}
				}
			}
		}
	}
}
