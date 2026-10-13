package k8s

import (
	"context"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// BootstrapNamespace must never touch cluster-scoped RBAC. Before #1406 it
// added tenant ServiceAccounts to shared ClusterRoleBindings, which broke
// tenant isolation.
func TestBootstrapNamespace_DoesNotGrantClusterRBAC(t *testing.T) {
	ctx := context.Background()
	names := []string{"review-sandbox", "issue-sandbox"}
	clientset := fake.NewSimpleClientset()
	for _, name := range names {
		crb := &rbacv1.ClusterRoleBinding{
			ObjectMeta: v1.ObjectMeta{Name: name},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: name},
		}
		if _, err := clientset.RbacV1().ClusterRoleBindings().Create(ctx, crb, v1.CreateOptions{}); err != nil {
			t.Fatalf("seeding ClusterRoleBinding %s: %v", name, err)
		}
	}
	clientset.ClearActions()

	if err := BootstrapNamespace(ctx, clientset, "alice"); err != nil {
		t.Fatalf("BootstrapNamespace: %v", err)
	}

	for _, a := range clientset.Actions() {
		if r := a.GetResource().Resource; r == "clusterrolebindings" || r == "clusterroles" {
			t.Errorf("BootstrapNamespace issued %s on %s", a.GetVerb(), r)
		}
	}
	for _, name := range names {
		crb, err := clientset.RbacV1().ClusterRoleBindings().Get(ctx, name, v1.GetOptions{})
		if err != nil {
			t.Fatalf("get ClusterRoleBinding %s: %v", name, err)
		}
		if len(crb.Subjects) != 0 {
			t.Errorf("ClusterRoleBinding %s gained subjects: %v", name, crb.Subjects)
		}
	}
}
