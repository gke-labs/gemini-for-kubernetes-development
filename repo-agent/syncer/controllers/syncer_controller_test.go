/*
Copyright 2025.

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

	syncerv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/syncer/api/v1alpha1"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

// MockUploader is a mock implementation of Uploader for testing purposes.
type MockUploader struct {
	UploadFunc func(ctx context.Context, bucket, path string, data []byte) error
}

func (m *MockUploader) Upload(ctx context.Context, bucket, path string, data []byte) error {
	if m.UploadFunc != nil {
		return m.UploadFunc(ctx, bucket, path, data)
	}
	return nil
}

func TestDynamicResourceReconciler_Reconcile(t *testing.T) {
	g := gomega.NewWithT(t)

	// Set up scheme
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = syncerv1alpha1.AddToScheme(scheme)

	// Define GVK we are testing
	gvk := schema.GroupVersionKind{
		Group:   "",
		Version: "v1",
		Kind:    "ConfigMap",
	}

	// Create a Syncer object
	syncer := &syncerv1alpha1.Syncer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-syncer",
			Namespace: "default",
		},
		Spec: syncerv1alpha1.SyncerSpec{
			InstallationName: "test-install",
			GCSBucketName:    "test-bucket",
			Rules: []syncerv1alpha1.ResourceRule{
				{
					Group:    "",
					Version:  "v1",
					Kind:     "ConfigMap",
					MatchCEL: "object.metadata.name == 'target-cm'",
				},
			},
		},
	}

	// Create target ConfigMap
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "target-cm",
			Namespace: "default",
			Labels: map[string]string{
				"sync": "true",
			},
		},
		Data: map[string]string{
			"key": "value",
		},
	}

	// Create ignored ConfigMap
	ignoredCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ignored-cm",
			Namespace: "default",
		},
	}

	// Create fake client with resources
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(syncer, cm, ignoredCM).
		Build()

	// Mock GCS Uploader
	uploaded := false
	var uploadedBucket, uploadedPath string
	mockGCS := &MockUploader{
		UploadFunc: func(_ context.Context, bucket, path string, _ []byte) error {
			uploaded = true
			uploadedBucket = bucket
			uploadedPath = path
			return nil
		},
	}

	// Create Reconciler
	r := &DynamicResourceReconciler{
		Client:    k8sClient,
		GVK:       gvk,
		GCSClient: mockGCS,
	}

	ctx := context.Background()

	// 1. Test Matching Resource
	req := ctrl.Request{
		NamespacedName: client.ObjectKey{
			Name:      "target-cm",
			Namespace: "default",
		},
	}

	_, err := r.Reconcile(ctx, req)
	g.Expect(err).ToNot(gomega.HaveOccurred())
	g.Expect(uploaded).To(gomega.BeTrue())
	g.Expect(uploadedBucket).To(gomega.Equal("test-bucket"))
	g.Expect(uploadedPath).To(gomega.Equal("test-install/ConfigMap/default/target-cm.yaml"))

	// 2. Test Ignored Resource (CEL mismatch)
	uploaded = false // Reset
	reqIgnored := ctrl.Request{
		NamespacedName: client.ObjectKey{
			Name:      "ignored-cm",
			Namespace: "default",
		},
	}

	_, err = r.Reconcile(ctx, reqIgnored)
	g.Expect(err).ToNot(gomega.HaveOccurred())
	g.Expect(uploaded).To(gomega.BeFalse())
}

func TestSyncerReconciler_Reconcile(t *testing.T) {
	// Simple test to ensure SyncerReconciler runs and attempts to set up watchers
	// Note: We can't fully test starting watchers with a fake manager easily in unit tests without envtest,
	// but we can verify the logic flows.

	// For this unit test, we'll just check if it reads the syncer object.
	// Testing the manager start is more of an integration test.

	g := gomega.NewWithT(t)
	scheme := runtime.NewScheme()
	_ = syncerv1alpha1.AddToScheme(scheme)

	syncer := &syncerv1alpha1.Syncer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-syncer",
			Namespace: "default",
		},
		Spec: syncerv1alpha1.SyncerSpec{
			Rules: []syncerv1alpha1.ResourceRule{
				{Group: "", Version: "v1", Kind: "Pod"},
			},
		},
	}

	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(syncer).Build()

	r := &SyncerReconciler{
		Client:      k8sClient,
		Scheme:      scheme,
		Manager:     nil, // This will cause panic if startWatcher is called and uses Manager
		GCSClient:   &MockUploader{},
		watchedGVKs: make(map[schema.GroupVersionKind]bool),
	}

	// To safely test Reconcile without a real Manager, we would need to mock the Manager or
	// refactor startWatcher to be injectable.
	// However, since we verified the core logic in DynamicResourceReconciler,
	// we will skip the deep integration test of SyncerReconciler in this unit test file
	// to avoid complexity with mocking ctrl.Manager which is an interface with many methods.

	// We can test that it doesn't crash on Not Found
	req := ctrl.Request{NamespacedName: client.ObjectKey{Name: "missing", Namespace: "default"}}
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))
	_, err := r.Reconcile(context.Background(), req)
	g.Expect(err).ToNot(gomega.HaveOccurred())
}
