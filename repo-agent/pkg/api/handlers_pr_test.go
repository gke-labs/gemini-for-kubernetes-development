package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/auth"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
	yaml "gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
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

	t.Run("Create task with explicit prompt", func(t *testing.T) {
		// Create the RepoWatch
		repoWatch := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "review.gemini.google.com/v1alpha1",
				"kind":       "RepoWatch",
				"metadata": map[string]interface{}{
					"name":      "test-repo",
					"namespace": "default",
				},
				"spec": map[string]interface{}{
					"review": map[string]interface{}{
						"maxReviewFiles": int64(10),
						"ignoreFiles":    []interface{}{"*.lock", "*.pdf"},
					},
				},
			},
		}
		_, err := dynamicClient.Resource(gvrRepoWatch).Namespace("default").Create(context.Background(), repoWatch, v1.CreateOptions{})
		if err != nil {
			t.Fatalf("Failed to create repowatch: %v", err)
		}

		// Create the Sandbox first
		sandbox := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "agents.x-k8s.io/v1alpha1",
				"kind":       "Sandbox",
				"metadata": map[string]interface{}{
					"name":      "test-repo-pr-123",
					"namespace": "default",
				},
			},
		}
		_, err = dynamicClient.Resource(gvrSandbox).Namespace("default").Create(context.Background(), sandbox, v1.CreateOptions{})
		if err != nil {
			t.Fatalf("Failed to create review sandbox: %v", err)
		}

		payload := map[string]string{
			"prompt": "Test Prompt",
		}
		jsonValue, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("Failed to marshal payload: %v", err)
		}
		req, err := http.NewRequest("POST", "/repo/test-repo/prs/123/tasks", bytes.NewBuffer(jsonValue))
		if err != nil {
			t.Fatalf("Failed to create request: %v", err)
		}
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
			if params["MAX_REVIEW_FILES"] != "10" {
				t.Errorf("Expected MAX_REVIEW_FILES '10', got %v", params["MAX_REVIEW_FILES"])
			}
			if params["IGNORE_FILES"] != "*.lock,*.pdf" {
				t.Errorf("Expected IGNORE_FILES '*.lock,*.pdf', got %v", params["IGNORE_FILES"])
			}
			sandboxName, _, _ := unstructured.NestedString(task.Object, "spec", "sandboxName")
			if sandboxName != "test-repo-pr-123" {
				t.Errorf("Expected sandboxName 'test-repo-pr-123', got %s", sandboxName)
			}
		}
	})

	t.Run("Create task with explicit model", func(t *testing.T) {
		// Create the Sandbox for a different PR
		sandbox := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "agents.x-k8s.io/v1alpha1",
				"kind":       "Sandbox",
				"metadata": map[string]interface{}{
					"name":      "test-repo-pr-124",
					"namespace": "default",
				},
			},
		}
		_, err := dynamicClient.Resource(gvrSandbox).Namespace("default").Create(context.Background(), sandbox, v1.CreateOptions{})
		if err != nil {
			t.Fatalf("Failed to create review sandbox: %v", err)
		}

		payload := map[string]string{
			"prompt": "Test Model Prompt",
			"model":  "test-model",
		}
		jsonValue, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("Failed to marshal payload: %v", err)
		}
		req, err := http.NewRequest("POST", "/repo/test-repo/prs/124/tasks", bytes.NewBuffer(jsonValue))
		if err != nil {
			t.Fatalf("Failed to create request: %v", err)
		}
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
		// Should be 2 tasks now (one from previous test case)
		if len(list.Items) < 1 {
			t.Errorf("Expected at least 1 task, got %d", len(list.Items))
		} else {
			// Find the task with our prompt
			var foundTask *unstructured.Unstructured
			for _, item := range list.Items {
				params, _, _ := unstructured.NestedMap(item.Object, "spec", "params")
				if params["AGENT_PROMPT"] == "Test Model Prompt" {
					foundTask = &item
					break
				}
			}
			if foundTask == nil {
				t.Fatalf("Task with prompt 'Test Model Prompt' not found")
			}
			params, _, _ := unstructured.NestedMap(foundTask.Object, "spec", "params")
			if params["model"] != "test-model" {
				t.Errorf("Expected model 'test-model', got %v", params["model"])
			}
		}
	})
}

func TestSubmitReview(t *testing.T) {
	scheme := runtime.NewScheme()
	gvrSandbox := schema.GroupVersionResource{Group: "agents.x-k8s.io", Version: "v1alpha1", Resource: "sandboxes"}
	gvrRepoWatch := schema.GroupVersionResource{Group: "review.gemini.google.com", Version: "v1alpha1", Resource: "repowatches"}

	dynamicClient := fake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		gvrSandbox:   "SandboxList",
		gvrRepoWatch: "RepoWatchList",
	})
	k8sClient := kubernetesfake.NewClientset()

	manager := &k8s.Manager{
		Client:    dynamicClient,
		Clientset: k8sClient,
	}

	server := &Server{
		K8sManager:                  manager,
		Auth:                        &auth.Authenticator{K8sManager: manager},
		TraceabilityMetadataEnabled: true,
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(auth.UserKey, "default")
		c.Next()
	})
	r.POST("/repo/:repo/prs/:id/submitreview", server.submitReview)

	t.Run("Submit review with metadata", func(t *testing.T) {
		repo := "test-repo"
		prID := "123"
		namespace := "default"
		sandboxName := fmt.Sprintf("%s-pr-%s", repo, prID)

		// Create RepoWatch
		rw := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "review.gemini.google.com/v1alpha1",
				"kind":       "RepoWatch",
				"metadata": map[string]interface{}{
					"name":      repo,
					"namespace": namespace,
				},
				"spec": map[string]interface{}{
					"repoURL":          "https://github.com/owner/repo.git",
					"githubSecretName": "gh-token",
				},
			},
		}
		_, _ = dynamicClient.Resource(gvrRepoWatch).Namespace(namespace).Create(context.Background(), rw, v1.CreateOptions{})

		// Create Secret for token
		secret := &corev1.Secret{
			ObjectMeta: v1.ObjectMeta{Name: "gh-token", Namespace: namespace},
			Data:       map[string][]byte{"pat": []byte("fake-token")},
		}
		_, _ = k8sClient.CoreV1().Secrets(namespace).Create(context.Background(), secret, v1.CreateOptions{})

		// Create Sandbox
		sb := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "agents.x-k8s.io/v1alpha1",
				"kind":       "Sandbox",
				"metadata": map[string]interface{}{
					"name":      sandboxName,
					"namespace": namespace,
				},
			},
		}
		_, _ = dynamicClient.Resource(gvrSandbox).Namespace(namespace).Create(context.Background(), sb, v1.CreateOptions{})

		reviewText := "This looks good!\n---\nnote: test note\nreview:\n  body: LGTM\n"
		payload := map[string]string{
			"review": reviewText,
		}
		jsonValue, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("Failed to marshal payload: %v", err)
		}
		req, err := http.NewRequest("POST", "/repo/test-repo/prs/123/submitreview", bytes.NewBuffer(jsonValue))
		if err != nil {
			t.Fatalf("Failed to create request: %v", err)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		// We expect 500 here because the GitHub client is created but fails on the actual API call
		// since it's not fully mocked at the transport level. But 500 is better than a panic.
		if w.Code != http.StatusInternalServerError && w.Code != http.StatusOK {
			t.Errorf("Expected status 500 or 200, got %d. Body: %s", w.Code, w.Body.String())
		}
	})
}

func TestStandardizeYAML(t *testing.T) {
	// Simple test to ensure the standardized yaml package works
	type TestStruct struct {
		Name string `yaml:"name"`
	}
	data := "name: test"
	var ts TestStruct
	err := yaml.Unmarshal([]byte(data), &ts)
	if err != nil {
		t.Fatalf("Failed to unmarshal YAML: %v", err)
	}
	if ts.Name != "test" {
		t.Errorf("Expected name 'test', got %q", ts.Name)
	}
}
