/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"reflect"
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	overseerv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/overseer/pkg/api/v1alpha1"
)

func setupTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = rbacv1.AddToScheme(s)
	_ = overseerv1alpha1.AddToScheme(s)
	return s
}

func TestOverseerReconciler_ObservedGeneration(t *testing.T) {
	scheme := setupTestScheme()
	ctx := context.Background()

	t.Run("Reconcile with missing secret sets Error status and observedGeneration", func(t *testing.T) {
		overseer := &overseerv1alpha1.Overseer{
			ObjectMeta: metav1.ObjectMeta{
				Name:       "test-overseer",
				Generation: 3,
			},
			Spec: overseerv1alpha1.OverseerSpec{
				RepoURL: "https://github.com/test/repo",
			},
		}

		k8sClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithStatusSubresource(&overseerv1alpha1.Overseer{}).
			WithObjects(overseer).
			Build()

		r := &OverseerReconciler{
			Client: k8sClient,
			Scheme: scheme,
		}

		res, err := r.Reconcile(ctx, ctrl.Request{
			NamespacedName: types.NamespacedName{Name: "test-overseer"},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.RequeueAfter != 5*time.Minute {
			t.Fatalf("expected RequeueAfter of 5m, got: %v", res.RequeueAfter)
		}

		var updated overseerv1alpha1.Overseer
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: "test-overseer"}, &updated); err != nil {
			t.Fatalf("failed to get updated overseer: %v", err)
		}

		if updated.Status.ObservedGeneration != 3 {
			t.Errorf("expected observedGeneration to be 3, got: %d", updated.Status.ObservedGeneration)
		}
		if updated.Status.OverseerStatus != "Error" {
			t.Errorf("expected overseerStatus to be Error, got: %s", updated.Status.OverseerStatus)
		}
	})

	t.Run("Reconcile successfully sets Active status and observedGeneration", func(t *testing.T) {
		overseer := &overseerv1alpha1.Overseer{
			ObjectMeta: metav1.ObjectMeta{
				Name:       "my-overseer",
				Generation: 5,
			},
			Spec: overseerv1alpha1.OverseerSpec{
				RepoURL:                "https://github.com/test/repo",
				GeminiAPIKeySecretName: "my-gemini-key",
			},
		}

		geminiSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "my-gemini-key",
				Namespace: "overseer-system",
			},
			Data: map[string][]byte{
				"GEMINI_API_KEY": []byte("test-key"),
			},
		}

		caSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "github-portal-ca",
				Namespace: "overseer-system",
			},
			Data: map[string][]byte{
				"ca.crt": []byte("test-ca"),
			},
		}

		k8sClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithStatusSubresource(&overseerv1alpha1.Overseer{}).
			WithObjects(overseer, geminiSecret, caSecret).
			Build()

		r := &OverseerReconciler{
			Client: k8sClient,
			Scheme: scheme,
		}

		res, err := r.Reconcile(ctx, ctrl.Request{
			NamespacedName: types.NamespacedName{Name: "my-overseer"},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res != (ctrl.Result{}) {
			t.Fatalf("expected empty result, got: %v", res)
		}

		var updated overseerv1alpha1.Overseer
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: "my-overseer"}, &updated); err != nil {
			t.Fatalf("failed to get updated overseer: %v", err)
		}

		if updated.Status.ObservedGeneration != 5 {
			t.Errorf("expected observedGeneration to be 5, got: %d", updated.Status.ObservedGeneration)
		}
		if updated.Status.OverseerStatus != "Active" {
			t.Errorf("expected overseerStatus to be Active, got: %s", updated.Status.OverseerStatus)
		}

		// Update generation and re-reconcile
		updated.Generation = 6
		updated.Spec.PollInterval = "10m"
		if err := k8sClient.Update(ctx, &updated); err != nil {
			t.Fatalf("failed to update overseer: %v", err)
		}

		res, err = r.Reconcile(ctx, ctrl.Request{
			NamespacedName: types.NamespacedName{Name: "my-overseer"},
		})
		if err != nil {
			t.Fatalf("unexpected error on second reconcile: %v", err)
		}
		if res != (ctrl.Result{}) {
			t.Fatalf("expected empty result, got: %v", res)
		}

		if err := k8sClient.Get(ctx, types.NamespacedName{Name: "my-overseer"}, &updated); err != nil {
			t.Fatalf("failed to get re-reconciled overseer: %v", err)
		}
		if updated.Status.ObservedGeneration != 6 {
			t.Errorf("expected observedGeneration to be updated to 6, got: %d", updated.Status.ObservedGeneration)
		}
	})

	t.Run("Reconcile non-existent overseer returns empty without error", func(t *testing.T) {
		k8sClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithStatusSubresource(&overseerv1alpha1.Overseer{}).
			Build()

		r := &OverseerReconciler{
			Client: k8sClient,
			Scheme: scheme,
		}

		res, err := r.Reconcile(ctx, ctrl.Request{
			NamespacedName: types.NamespacedName{Name: "does-not-exist"},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res != (ctrl.Result{}) {
			t.Fatalf("expected empty result, got: %v", res)
		}
	})
}

func TestEnsureOverseerRBAC_NoClusterWideGrant(t *testing.T) {
	scheme := setupTestScheme()
	ctx := context.Background()
	const ns = "overseer-my-overseer"

	o := &overseerv1alpha1.Overseer{
		ObjectMeta: metav1.ObjectMeta{Name: "my-overseer", UID: "uid-1"},
	}
	staticSubject := rbacv1.Subject{Kind: "ServiceAccount", Name: "overseer", Namespace: "overseer-system"}
	staticSandboxSubject := rbacv1.Subject{Kind: "ServiceAccount", Name: "overseer-sandbox", Namespace: "overseer-system"}
	userSubject := rbacv1.Subject{Kind: "User", Name: "overseer-admin"}

	// State left behind by earlier controller versions.
	overseerBinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "overseer-binding"},
		Subjects: []rbacv1.Subject{
			staticSubject,
			{Kind: "ServiceAccount", Name: "overseer", Namespace: ns},
			{Kind: "ServiceAccount", Name: "overseer", Namespace: "overseer-deleted-one"},
			userSubject,
		},
		RoleRef: rbacv1.RoleRef{Kind: "ClusterRole", Name: "overseer", APIGroup: "rbac.authorization.k8s.io"},
	}
	sandboxBinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "overseer-sandbox"},
		Subjects: []rbacv1.Subject{
			staticSandboxSubject,
			{Kind: "ServiceAccount", Name: "overseer-sandbox", Namespace: ns},
		},
		RoleRef: rbacv1.RoleRef{Kind: "ClusterRole", Name: "overseer-sandbox", APIGroup: "rbac.authorization.k8s.io"},
	}
	legacySA := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "overseer-sandbox", Namespace: ns}}
	legacyRB := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "overseer-sandbox-binding", Namespace: ns},
		RoleRef:    rbacv1.RoleRef{Kind: "ClusterRole", Name: "overseer-sandbox", APIGroup: "rbac.authorization.k8s.io"},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(o, overseerBinding, sandboxBinding, legacySA, legacyRB).
		Build()
	r := &OverseerReconciler{Client: k8sClient, Scheme: scheme}

	// Run twice: the second pass must be a no-op.
	for i := 0; i < 2; i++ {
		if err := r.ensureOverseerRBAC(ctx, o, ns); err != nil {
			t.Fatalf("ensureOverseerRBAC (pass %d): %v", i, err)
		}
	}

	// No per-Overseer subject is left in a shared ClusterRoleBinding; static
	// and non-ServiceAccount subjects are kept.
	var crbs rbacv1.ClusterRoleBindingList
	if err := k8sClient.List(ctx, &crbs); err != nil {
		t.Fatal(err)
	}
	for _, crb := range crbs.Items {
		if crb.Name == clusterReaderBindingName(ns) {
			continue
		}
		for _, s := range crb.Subjects {
			if isLegacyOverseerSubject(s) {
				t.Errorf("ClusterRoleBinding %s still grants %s/%s", crb.Name, s.Namespace, s.Name)
			}
		}
	}
	var got rbacv1.ClusterRoleBinding
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "overseer-binding"}, &got); err != nil {
		t.Fatal(err)
	}
	if want := []rbacv1.Subject{staticSubject, userSubject}; !reflect.DeepEqual(got.Subjects, want) {
		t.Errorf("overseer-binding subjects = %v, want %v", got.Subjects, want)
	}
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "overseer-sandbox"}, &got); err != nil {
		t.Fatal(err)
	}
	if want := []rbacv1.Subject{staticSandboxSubject}; !reflect.DeepEqual(got.Subjects, want) {
		t.Errorf("overseer-sandbox subjects = %v, want %v", got.Subjects, want)
	}

	// The per-Overseer binding grants only overseer-cluster-reader, only to
	// this Overseer's ServiceAccount, and is owned by the Overseer.
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: clusterReaderBindingName(ns)}, &got); err != nil {
		t.Fatalf("per-Overseer ClusterRoleBinding: %v", err)
	}
	if got.RoleRef.Kind != "ClusterRole" || got.RoleRef.Name != "overseer-cluster-reader" {
		t.Errorf("roleRef = %+v, want ClusterRole overseer-cluster-reader", got.RoleRef)
	}
	if want := []rbacv1.Subject{{Kind: "ServiceAccount", Name: "overseer", Namespace: ns}}; !reflect.DeepEqual(got.Subjects, want) {
		t.Errorf("subjects = %v, want %v", got.Subjects, want)
	}
	if len(got.OwnerReferences) != 1 || got.OwnerReferences[0].UID != o.UID {
		t.Errorf("ownerReferences = %v, want the Overseer", got.OwnerReferences)
	}

	// The namespaced RoleBinding to ClusterRole overseer is still there.
	var rb rbacv1.RoleBinding
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "overseer-binding", Namespace: ns}, &rb); err != nil {
		t.Fatalf("namespaced RoleBinding overseer-binding: %v", err)
	}

	// The unused overseer-sandbox ServiceAccount and RoleBinding are gone.
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "overseer-sandbox", Namespace: ns}, &corev1.ServiceAccount{}); !apierrors.IsNotFound(err) {
		t.Errorf("overseer-sandbox ServiceAccount: got err %v, want NotFound", err)
	}
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "overseer-sandbox-binding", Namespace: ns}, &rbacv1.RoleBinding{}); !apierrors.IsNotFound(err) {
		t.Errorf("overseer-sandbox-binding RoleBinding: got err %v, want NotFound", err)
	}
}

func TestEnsureClusterReaderBinding_ResetsDrift(t *testing.T) {
	scheme := setupTestScheme()
	ctx := context.Background()
	const ns = "overseer-my-overseer"

	o := &overseerv1alpha1.Overseer{ObjectMeta: metav1.ObjectMeta{Name: "my-overseer", UID: "uid-1"}}
	drifted := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: clusterReaderBindingName(ns)},
		Subjects: []rbacv1.Subject{
			{Kind: "ServiceAccount", Name: "overseer", Namespace: ns},
			{Kind: "ServiceAccount", Name: "intruder", Namespace: "default"},
		},
		RoleRef: rbacv1.RoleRef{Kind: "ClusterRole", Name: "cluster-admin", APIGroup: "rbac.authorization.k8s.io"},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(o, drifted).Build()
	r := &OverseerReconciler{Client: k8sClient, Scheme: scheme}

	if err := r.ensureClusterReaderBinding(ctx, o, ns); err != nil {
		t.Fatal(err)
	}
	var got rbacv1.ClusterRoleBinding
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: clusterReaderBindingName(ns)}, &got); err != nil {
		t.Fatal(err)
	}
	if got.RoleRef.Name != "overseer-cluster-reader" {
		t.Errorf("roleRef = %+v, want overseer-cluster-reader", got.RoleRef)
	}
	if want := []rbacv1.Subject{{Kind: "ServiceAccount", Name: "overseer", Namespace: ns}}; !reflect.DeepEqual(got.Subjects, want) {
		t.Errorf("subjects = %v, want %v", got.Subjects, want)
	}
}

func TestOverseersForLegacyBinding(t *testing.T) {
	scheme := setupTestScheme()
	ctx := context.Background()

	a := &overseerv1alpha1.Overseer{ObjectMeta: metav1.ObjectMeta{Name: "a"}}
	b := &overseerv1alpha1.Overseer{ObjectMeta: metav1.ObjectMeta{Name: "b"}}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(a, b).Build()
	r := &OverseerReconciler{Client: k8sClient, Scheme: scheme}

	static := rbacv1.Subject{Kind: "ServiceAccount", Name: "overseer", Namespace: "overseer-system"}
	leaked := rbacv1.Subject{Kind: "ServiceAccount", Name: "overseer", Namespace: "overseer-a"}

	tests := []struct {
		name     string
		crb      *rbacv1.ClusterRoleBinding
		watched  bool
		wantReqs []string
	}{
		{
			name:     "legacy binding with per-Overseer subject enqueues every Overseer",
			crb:      &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "overseer-binding"}, Subjects: []rbacv1.Subject{static, leaked}},
			watched:  true,
			wantReqs: []string{"a", "b"},
		},
		{
			name:    "legacy binding with only static subjects enqueues nothing",
			crb:     &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "overseer-sandbox"}, Subjects: []rbacv1.Subject{static}},
			watched: true,
		},
		{
			name:    "unrelated binding is filtered out",
			crb:     &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "cluster-admin"}, Subjects: []rbacv1.Subject{leaked}},
			watched: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isLegacyClusterRoleBinding(tc.crb); got != tc.watched {
				t.Fatalf("isLegacyClusterRoleBinding = %v, want %v", got, tc.watched)
			}
			if !tc.watched {
				return
			}
			var got []string
			for _, req := range r.overseersForLegacyBinding(ctx, tc.crb) {
				got = append(got, req.Name)
			}
			slices.Sort(got)
			if !slices.Equal(got, tc.wantReqs) {
				t.Errorf("requests = %v, want %v", got, tc.wantReqs)
			}
		})
	}
}
