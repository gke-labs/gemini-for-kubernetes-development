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
	"context"
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
	"k8s.io/client-go/dynamic/fake"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
)

func TestCancelTask(t *testing.T) {
	scheme := runtime.NewScheme()
	gvrSandboxTask := schema.GroupVersionResource{Group: "custom.agents.x-k8s.io", Version: "v1alpha1", Resource: "sandboxtasks"}

	dynamicClient := fake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		gvrSandboxTask: "SandboxTaskList",
	})
	k8sClient := kubernetesfake.NewClientset()

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

	r.Use(func(c *gin.Context) {
		c.Set(auth.UserKey, "default")
		c.Next()
	})

	r.POST("/api/repo/:repo/tasks/:taskID/cancel", server.cancelTask)

	t.Run("Cancel existing task", func(t *testing.T) {
		// Create the SandboxTask first
		task := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "custom.agents.x-k8s.io/v1alpha1",
				"kind":       "SandboxTask",
				"metadata": map[string]interface{}{
					"name":      "test-task-123",
					"namespace": "default",
				},
				"status": map[string]interface{}{
					"taskState": "Running",
				},
			},
		}
		_, err := dynamicClient.Resource(gvrSandboxTask).Namespace("default").Create(context.Background(), task, v1.CreateOptions{})
		if err != nil {
			t.Fatalf("Failed to create SandboxTask: %v", err)
		}

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/api/repo/test-repo/tasks/test-task-123/cancel", nil)
		r.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Expected status 200, got %d", w.Code)
		}

		// Retrieve the updated task to check status
		updatedTask, err := dynamicClient.Resource(gvrSandboxTask).Namespace("default").Get(context.Background(), "test-task-123", v1.GetOptions{})
		if err != nil {
			t.Fatalf("Failed to get updated task: %v", err)
		}

		status, found, err := unstructured.NestedMap(updatedTask.Object, "status")
		if err != nil || !found {
			t.Fatalf("Failed to get status map: %v", err)
		}

		if status["taskState"] != "Cancelled" {
			t.Errorf("Expected status.taskState to be 'Cancelled', got '%v'", status["taskState"])
		}
		if status["result"] != "task cancelled" {
			t.Errorf("Expected status.result to be 'task cancelled', got '%v'", status["result"])
		}
	})
}
