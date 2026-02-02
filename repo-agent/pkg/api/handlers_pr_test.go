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

func TestCreatePRTask(t *testing.T) {
	scheme := runtime.NewScheme()
	gvrSandboxTask := schema.GroupVersionResource{Group: "custom.agents.x-k8s.io", Version: "v1alpha1", Resource: "sandboxtasks"}
	gvrReviewSandbox := schema.GroupVersionResource{Group: "custom.agents.x-k8s.io", Version: "v1alpha1", Resource: "reviewsandboxes"}

	dynamicClient := fake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		gvrSandboxTask:   "SandboxTaskList",
		gvrReviewSandbox: "ReviewSandboxList",
	})
	k8sClient := kubernetesfake.NewSimpleClientset()

	dynamicClient.PrependReactor("patch", "reviewsandboxes", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
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

	t.Run("Create task with explicit prompt", func(t *testing.T) {
		// Create the ReviewSandbox first
		sandbox := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "custom.agents.x-k8s.io/v1alpha1",
				"kind":       "ReviewSandbox",
				"metadata": map[string]interface{}{
					"name":      "test-repo-pr-123",
					"namespace": "default",
				},
			},
		}
		_, err := dynamicClient.Resource(gvrReviewSandbox).Namespace("default").Create(context.Background(), sandbox, v1.CreateOptions{})
		if err != nil {
			t.Fatalf("Failed to create review sandbox: %v", err)
		}

		payload := map[string]string{
			"prompt": "Test Prompt",
		}
		jsonValue, _ := json.Marshal(payload)
		req, _ := http.NewRequest("POST", "/repo/test-repo/prs/123/tasks", bytes.NewBuffer(jsonValue))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Expected status 200, got %d. Body: %s", w.Code, w.Body.String())
		}

		gvr := schema.GroupVersionResource{
			Group:    "custom.agents.x-k8s.io",
			Version:  "v1alpha1",
			Resource: "sandboxtasks",
		}
		list, err := dynamicClient.Resource(gvr).Namespace("default").List(context.Background(), v1.ListOptions{})
		if err != nil {
			t.Fatalf("Failed to list tasks: %v", err)
		}
		if len(list.Items) != 1 {
			t.Errorf("Expected 1 task, got %d", len(list.Items))
		} else {
			task := list.Items[0]
			params, _, _ := unstructured.NestedMap(task.Object, "spec", "params")
			if params["AGENT_PROMPT"] != "Test Prompt" {
				t.Errorf("Expected prompt 'Test Prompt', got %v", params["AGENT_PROMPT"])
			}
			sandboxName, _, _ := unstructured.NestedString(task.Object, "spec", "sandboxName")
			if sandboxName != "test-repo-pr-123" {
				t.Errorf("Expected sandboxName 'test-repo-pr-123', got %s", sandboxName)
			}
		}
	})
}
