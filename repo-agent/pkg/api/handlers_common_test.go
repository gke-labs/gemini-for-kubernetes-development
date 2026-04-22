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
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/fake"

	sandboxtaskv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/sandboxtask/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/auth"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
)

func TestTruncateString(t *testing.T) {
	tests := []struct {
		name  string
		s     string
		limit int
		want  string
	}{
		{
			name:  "No truncation needed",
			s:     "hello",
			limit: 10,
			want:  "hello",
		},
		{
			name:  "Exact limit",
			s:     "hello",
			limit: 5,
			want:  "hello",
		},
		{
			name:  "Basic truncation with buffer",
			s:     "hello world how are you",
			limit: 15,
			want:  "hello world how",
		},
		{
			name:  "Truncation with code block",
			s:     "```\nsome code\n```\nmore text",
			limit: 15,
			want:  "```\nsome co\n```",
		},
		{
			name:  "UTF-8 truncation - safe",
			s:     "世界", // 6 bytes
			limit: 16,
			want:  "世界",
		},
		{
			name:  "UTF-8 truncation - middle of rune",
			s:     "世界 hello world", // 6 bytes + 12 = 18 bytes
			limit: 13,
			want:  "世界 hello ",
		},
		{
			name:  "Empty string with small limit",
			s:     "",
			limit: 5,
			want:  "[Bot-",
		},
		{
			name:  "Zero limit",
			s:     "hello",
			limit: 0,
			want:  "",
		},
		{
			name:  "Tilde code block",
			s:     "~~~\nsome code\n~~~\nmore text",
			limit: 15,
			want:  "~~~\nsome co\n~~~",
		},
		{
			name:  "UTF-8 boundary - 世 (3 bytes)",
			s:     "世界",
			limit: 3,
			want:  "世",
		},
		{
			name:  "UTF-8 boundary - middle of 世 (limit 2)",
			s:     "世界",
			limit: 2,
			want:  "[B",
		},
		{
			name:  "UTF-8 boundary - middle of 界 (limit 4)",
			s:     "世界",
			limit: 4,
			want:  "世",
		},
		{
			name:  "Multiple code blocks - close all",
			s:     "```go\nfunc a() {}\n```\nSome text\n```python\ndef b(): pass",
			limit: 40,
			want:  "```go\nfunc a() {}\n```\nSome text\n```p\n```",
		},
		{
			name:  "Nested code blocks (rare but possible)",
			s:     "``\n```go\ncode\n```\n``",
			limit: 10,
			want:  "``\n```\n```",
		},
		{
			name:  "Nested code blocks - correct closure order",
			s:     "```\n~~~\ncode long long string",
			limit: 25,
			want:  "```\n~~~\ncode long lon\n```",
		},
		{
			name:  "Nested code blocks - reverse order",
			s:     "~~~\n```\ncode long long string",
			limit: 25,
			want:  "~~~\n```\ncode long lon\n~~~",
		},
		{
			name:  "Emoji boundary - 4 bytes",
			s:     "Hello 🌟 world", // 🌟 is 4 bytes
			limit: 9,               // "Hello " (6) + 3 bytes of 🌟
			want:  "Hello ",
		},
		{
			name:  "Emoji boundary - exact fit",
			s:     "Hello 🌟 world",
			limit: 10, // "Hello " (6) + 4 bytes of 🌟
			want:  "Hello 🌟",
		},
		{
			name:  "Extremely small limit with open block",
			s:     "```\nsome code",
			limit: 3,
			want:  "[Bo", // Should return truncated fallback
		},
		{
			name:  "Limit too small for closing tags but not for fallback",
			s:     "```\nsome long code string",
			limit: 20,
			want:  "```\nsome long co\n```",
		},
		{
			name:  "Limit exactly size of closing tag",
			s:     "```\nlong code string",
			limit: 4,
			want:  "[Bot",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := truncateString(tt.s, tt.limit); got != tt.want {
				t.Errorf("truncateString() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGetRepoWatchName(t *testing.T) {
	scheme := runtime.NewScheme()
	gvrSandbox := schema.GroupVersionResource{Group: "agents.x-k8s.io", Version: "v1alpha1", Resource: "sandboxes"}

	dynamicClient := fake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		gvrSandbox: "SandboxList",
	})

	manager := &k8s.Manager{
		Client: dynamicClient,
	}

	server := &Server{
		K8sManager: manager,
	}

	ctx := context.Background()
	namespace := "default"
	sandboxName := "test-sandbox"

	t.Run("Sandbox exists with repowatch label", func(t *testing.T) {
		sb := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "agents.x-k8s.io/v1alpha1",
				"kind":       "Sandbox",
				"metadata": map[string]interface{}{
					"name":      sandboxName,
					"namespace": namespace,
					"labels": map[string]interface{}{
						"review.gemini.google.com/repowatch": "test-repowatch",
					},
				},
			},
		}
		_, _ = dynamicClient.Resource(gvrSandbox).Namespace(namespace).Create(ctx, sb, metav1.CreateOptions{})

		got := server.getRepoWatchName(ctx, namespace, sandboxName)
		if got != "test-repowatch" {
			t.Errorf("getRepoWatchName() = %q, want %q", got, "test-repowatch")
		}
	})

	t.Run("Sandbox does not exist", func(t *testing.T) {
		got := server.getRepoWatchName(ctx, namespace, "non-existent")
		if got != "n/a" {
			t.Errorf("getRepoWatchName() = %q, want %q", got, "n/a")
		}
	})

	t.Run("Sandbox lacks repowatch label", func(t *testing.T) {
		sandboxNameNoLabel := "no-label"
		sb := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "agents.x-k8s.io/v1alpha1",
				"kind":       "Sandbox",
				"metadata": map[string]interface{}{
					"name":      sandboxNameNoLabel,
					"namespace": namespace,
				},
			},
		}
		_, _ = dynamicClient.Resource(gvrSandbox).Namespace(namespace).Create(ctx, sb, metav1.CreateOptions{})

		got := server.getRepoWatchName(ctx, namespace, sandboxNameNoLabel)
		if got != "n/a" {
			t.Errorf("getRepoWatchName() = %q, want %q", got, "n/a")
		}
	})
}

func TestGetTaskMetadata(t *testing.T) {
	scheme := runtime.NewScheme()
	gvrSandboxTask := schema.GroupVersionResource{Group: "custom.agents.x-k8s.io", Version: "v1alpha1", Resource: "sandboxtasks"}

	dynamicClient := fake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		gvrSandboxTask: "SandboxTaskList",
	})

	manager := &k8s.Manager{
		Client: dynamicClient,
	}

	server := &Server{
		K8sManager: manager,
	}

	ctx := context.Background()
	namespace := "default"
	sandboxName := "test-sandbox"

	t.Run("Explicit task metadata provided", func(t *testing.T) {
		gotName, gotUID := server.getTaskMetadata(ctx, namespace, sandboxName, "task1", "uid1")
		if gotName != "task1" || gotUID != "uid1" {
			t.Errorf("getTaskMetadata() = (%q, %q), want (%q, %q)", gotName, gotUID, "task1", "uid1")
		}
	})

	t.Run("Explicit n/a provided - no fallback", func(t *testing.T) {
		gotName, gotUID := server.getTaskMetadata(ctx, namespace, sandboxName, "n/a", "n/a")
		if gotName != "n/a" || gotUID != "n/a" {
			t.Errorf("getTaskMetadata() with n/a = (%q, %q), want (%q, %q)", gotName, gotUID, "n/a", "n/a")
		}
	})

	t.Run("Resolve task UID from name", func(t *testing.T) {
		task := &sandboxtaskv1alpha1.SandboxTask{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "custom.agents.x-k8s.io/v1alpha1",
				Kind:       "SandboxTask",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      "task-to-resolve",
				Namespace: namespace,
				UID:       types.UID("resolved-uid"),
				Labels: map[string]string{
					"sandbox.gemini.google.com/sandbox-name": sandboxName,
				},
			},
		}
		unstructuredTask, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(task)
		_, _ = dynamicClient.Resource(gvrSandboxTask).Namespace(namespace).Create(ctx, &unstructured.Unstructured{Object: unstructuredTask}, metav1.CreateOptions{})

		gotName, gotUID := server.getTaskMetadata(ctx, namespace, sandboxName, "task-to-resolve", "")
		expectedName := fmt.Sprintf("%s/%s", namespace, "task-to-resolve")
		if gotName != expectedName || gotUID != "resolved-uid" {
			t.Errorf("getTaskMetadata() resolve = (%q, %q), want (%q, %q)", gotName, gotUID, expectedName, "resolved-uid")
		}
	})

	t.Run("Resolve task name from UID", func(t *testing.T) {
		task := &sandboxtaskv1alpha1.SandboxTask{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "custom.agents.x-k8s.io/v1alpha1",
				Kind:       "SandboxTask",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      "task-with-uid",
				Namespace: namespace,
				UID:       types.UID("specific-uid"),
				Labels: map[string]string{
					"sandbox.gemini.google.com/sandbox-name": sandboxName,
				},
			},
		}
		unstructuredTask, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(task)
		_, _ = dynamicClient.Resource(gvrSandboxTask).Namespace(namespace).Create(ctx, &unstructured.Unstructured{Object: unstructuredTask}, metav1.CreateOptions{})

		gotName, gotUID := server.getTaskMetadata(ctx, namespace, sandboxName, "n/a", "specific-uid")
		expectedName := fmt.Sprintf("%s/%s", namespace, "task-with-uid")
		if gotName != expectedName || gotUID != "specific-uid" {
			t.Errorf("getTaskMetadata() resolve name = (%q, %q), want (%q, %q)", gotName, gotUID, expectedName, "specific-uid")
		}
	})

	t.Run("Fallback to latest task", func(t *testing.T) {
		// Create a few tasks
		now := time.Now()
		for i := 1; i <= 3; i++ {
			task := &sandboxtaskv1alpha1.SandboxTask{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "custom.agents.x-k8s.io/v1alpha1",
					Kind:       "SandboxTask",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:              fmt.Sprintf("task-%d", i),
					Namespace:         namespace,
					UID:               types.UID(fmt.Sprintf("uid-%d", i)),
					CreationTimestamp: metav1.Time{Time: now.Add(time.Duration(i) * time.Minute)},
					Labels: map[string]string{
						"sandbox.gemini.google.com/sandbox-name": sandboxName,
					},
				},
			}
			unstructuredTask, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(task)
			_, _ = dynamicClient.Resource(gvrSandboxTask).Namespace(namespace).Create(ctx, &unstructured.Unstructured{Object: unstructuredTask}, metav1.CreateOptions{})
		}

		gotName, gotUID := server.getTaskMetadata(ctx, namespace, sandboxName, "", "")
		expectedName := fmt.Sprintf("%s/%s", namespace, "task-3")
		expectedUID := "uid-3"
		if gotName != expectedName || gotUID != expectedUID {
			t.Errorf("getTaskMetadata() fallback = (%q, %q), want (%q, %q)", gotName, gotUID, expectedName, expectedUID)
		}
	})

	t.Run("Invalid sandbox name - preserves provided metadata", func(t *testing.T) {
		gotName, gotUID := server.getTaskMetadata(ctx, namespace, "", "task1", "uid1")
		if gotName != "task1" || gotUID != "uid1" {
			t.Errorf("getTaskMetadata() with empty sandbox = (%q, %q), want (%q, %q)", gotName, gotUID, "task1", "uid1")
		}
	})
}

func TestApplyTraceabilityMetadata(t *testing.T) {
	scheme := runtime.NewScheme()
	gvrSandbox := schema.GroupVersionResource{Group: "agents.x-k8s.io", Version: "v1alpha1", Resource: "sandboxes"}
	gvrRepoWatch := schema.GroupVersionResource{Group: "review.gemini.google.com", Version: "v1alpha1", Resource: "repowatches"}

	dynamicClient := fake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		gvrSandbox:   "SandboxList",
		gvrRepoWatch: "RepoWatchList",
	})

	manager := &k8s.Manager{
		Client: dynamicClient,
	}

	server := &Server{
		K8sManager: manager,
	}

	gin.SetMode(gin.TestMode)
	// Need to mock s.Auth.GetNamespaceFromContext(c)
	// Authenticator is a struct, let's see how to mock it or just set up context.
	server.Auth = &auth.Authenticator{}

	body := "Test body"
	taskType := "test-task"
	sandboxName := "test-sandbox"

	t.Run("Traceability disabled", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request, _ = http.NewRequest("POST", "/", nil)

		server.TraceabilityMetadataEnabled = false
		got := server.applyTraceabilityMetadata(c, body, taskType, sandboxName, "n/a", "n/a")
		if got != body {
			t.Errorf("applyTraceabilityMetadata() disabled = %q, want %q", got, body)
		}
	})

	t.Run("Traceability enabled - adds footer", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request, _ = http.NewRequest("POST", "/", nil)

		server.TraceabilityMetadataEnabled = true
		got := server.applyTraceabilityMetadata(c, body, taskType, sandboxName, "task1", "uid1")
		if !strings.Contains(got, "<!-- repo-agent-metadata") {
			t.Errorf("applyTraceabilityMetadata() enabled = %q, missing footer", got)
		}
		if !strings.Contains(got, "task-type: test-task") {
			t.Errorf("applyTraceabilityMetadata() enabled = %q, missing task-type", got)
		}
	})

	t.Run("Traceability enabled - replaces existing footer and truncates correctly", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request, _ = http.NewRequest("POST", "/", nil)

		server.TraceabilityMetadataEnabled = true
		bodyWithFooter := body + "\n<!-- repo-agent-metadata\nold-data: some-value\n-->"
		got := server.applyTraceabilityMetadata(c, bodyWithFooter, taskType, sandboxName, "task1", "uid1")
		if strings.Count(got, "<!-- repo-agent-metadata") != 1 {
			t.Errorf("applyTraceabilityMetadata() duplicated footer = %q", got)
		}
		if strings.Contains(got, "old-data: some-value") {
			t.Errorf("applyTraceabilityMetadata() failed to remove old footer: %q", got)
		}
		if !strings.Contains(got, "task-type: test-task") {
			t.Errorf("applyTraceabilityMetadata() missing new metadata: %q", got)
		}
	})
}
