package sandbox

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
)

// The deployer service account is made only when it is missing: one that
// exists is left alone, and a read the account may not make (no get on
// service accounts) is reported as that, without a create - which used to
// fail with "creating factory-deployer service account: ... forbidden" for
// an account that existed all along, every week, for the retro.
func TestEnsureDeployerServiceAccount(t *testing.T) {
	ctx := context.Background()
	const ns = "overseer-kcc"
	creates := func(cs *k8sfake.Clientset) int {
		n := 0
		for _, a := range cs.Actions() {
			if a.GetVerb() == "create" && a.GetResource().Resource == "serviceaccounts" {
				n++
			}
		}
		return n
	}

	t.Run("exists: nothing made", func(t *testing.T) {
		cs := k8sfake.NewSimpleClientset(&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: DeployerServiceAccount, Namespace: ns}})
		if err := ensureDeployerServiceAccount(ctx, &clients.KubernetesClient{Clientset: cs}, ns); err != nil {
			t.Fatal(err)
		}
		if n := creates(cs); n != 0 {
			t.Errorf("%d creates for an account that exists", n)
		}
	})

	t.Run("missing: made", func(t *testing.T) {
		cs := k8sfake.NewSimpleClientset()
		if err := ensureDeployerServiceAccount(ctx, &clients.KubernetesClient{Clientset: cs}, ns); err != nil {
			t.Fatal(err)
		}
		if _, err := cs.CoreV1().ServiceAccounts(ns).Get(ctx, DeployerServiceAccount, metav1.GetOptions{}); err != nil {
			t.Errorf("not made: %v", err)
		}
	})

	t.Run("may not read: that error, no create", func(t *testing.T) {
		cs := k8sfake.NewSimpleClientset()
		cs.PrependReactor("get", "serviceaccounts", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "serviceaccounts"}, DeployerServiceAccount, nil)
		})
		err := ensureDeployerServiceAccount(ctx, &clients.KubernetesClient{Clientset: cs}, ns)
		if err == nil || !strings.Contains(err.Error(), "reading the factory-deployer service account") {
			t.Errorf("err = %v, want the read's error", err)
		}
		if n := creates(cs); n != 0 {
			t.Errorf("%d creates after a forbidden read", n)
		}
	})
}
