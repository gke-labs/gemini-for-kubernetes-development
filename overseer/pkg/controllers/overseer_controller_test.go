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
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
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
