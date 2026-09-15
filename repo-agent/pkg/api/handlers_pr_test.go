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

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/auth"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/fake"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// TestCreatePRTask covers the "Review Again" endpoint: with the factory CLI
// as the review engine, it marks the factory sandbox for re-review (the
// repowatch controller relaunches `factory pr review` on its next reconcile).
func TestCreatePRTask(t *testing.T) {
	scheme := runtime.NewScheme()
	gvrSandboxTask := schema.GroupVersionResource{Group: "custom.agents.x-k8s.io", Version: "v1alpha1", Resource: "sandboxtasks"}
	gvrSandbox := schema.GroupVersionResource{Group: "agents.x-k8s.io", Version: "v1alpha1", Resource: "sandboxes"}
	gvrRepoWatch := schema.GroupVersionResource{Group: "review.gemini.google.com", Version: "v1alpha1", Resource: "repowatches"}

	dynamicClient := fake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		gvrSandboxTask: "SandboxTaskList",
		gvrSandbox:     "SandboxList",
		gvrRepoWatch:   "RepoWatchList",
	})
	k8sClient := kubernetesfake.NewClientset()

	dynamicClient.PrependReactor("patch", "sandboxes", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
		patchAction := action.(k8stesting.PatchAction)
		if patchAction.GetPatchType() == types.ApplyPatchType {
			return true, nil, nil
		}
		return false, nil, nil
	})

	manager := &k8s.Manager{
		Client:    dynamicClient,
		Clientset: k8sClient,
	}

	server := &Server{
		K8sManager: manager,
		Auth: &auth.Authenticator{
			K8sManager: manager,
		},
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()

	// Mock Auth middleware by setting user
	r.Use(func(c *gin.Context) {
		c.Set(auth.UserKey, "default")
		c.Next()
	})

	r.POST("/repo/:repo/prs/:id/tasks", server.createPRTask)

	t.Run("Marks factory sandbox for re-review", func(t *testing.T) {
		sandbox := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "agents.x-k8s.io/v1alpha1",
				"kind":       "Sandbox",
				"metadata": map[string]interface{}{
					"name":      "factory-pr-123",
					"namespace": "default",
					"labels": map[string]interface{}{
						"factory.gemini.google.com/managed":  "true",
						"factory.gemini.google.com/pr":       "123",
						"review.gemini.google.com/repowatch": "test-repo",
					},
					"annotations": map[string]interface{}{
						"agentDraft": "review:\n  body: old draft",
					},
				},
			},
		}
		if _, err := dynamicClient.Resource(gvrSandbox).Namespace("default").Create(context.Background(), sandbox, v1.CreateOptions{}); err != nil {
			t.Fatalf("Failed to create review sandbox: %v", err)
		}

		payload := map[string]string{"prompt": "ignored override"}
		jsonValue, _ := json.Marshal(payload)
		req, _ := http.NewRequest("POST", "/repo/test-repo/prs/123/tasks", bytes.NewBuffer(jsonValue))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Expected status 200, got %d. Body: %s", w.Code, w.Body.String())
		}

		updated, err := dynamicClient.Resource(gvrSandbox).Namespace("default").Get(context.Background(), "factory-pr-123", v1.GetOptions{})
		if err != nil {
			t.Fatalf("Failed to get sandbox: %v", err)
		}
		if updated.GetAnnotations()["review.gemini.google.com/rereview-requested-at"] == "" {
			t.Errorf("Expected rereview-requested-at annotation to be set, got annotations: %v", updated.GetAnnotations())
		}

		// No legacy SandboxTask may be created.
		list, err := dynamicClient.Resource(gvrSandboxTask).Namespace("default").List(context.Background(), v1.ListOptions{})
		if err != nil {
			t.Fatalf("Failed to list tasks: %v", err)
		}
		if len(list.Items) != 0 {
			t.Errorf("Expected no SandboxTasks, got %d", len(list.Items))
		}
	})

	t.Run("Missing sandbox returns 404", func(t *testing.T) {
		payload := map[string]string{}
		jsonValue, _ := json.Marshal(payload)
		req, _ := http.NewRequest("POST", "/repo/test-repo/prs/999/tasks", bytes.NewBuffer(jsonValue))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if w.Code != http.StatusNotFound {
			t.Errorf("Expected status 404, got %d. Body: %s", w.Code, w.Body.String())
		}
	})
}
