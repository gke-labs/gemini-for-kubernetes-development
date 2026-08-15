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

package taskrunner

import (
	"context"
	"os"
	"testing"
	"time"

	sandboxtaskv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/sandboxtask/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/agentoutput"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/agentserver"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
)

func TestTaskRunner_Cancellation(t *testing.T) {
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

	// Create temp dir for tasks and logs
	tempDir, err := os.MkdirTemp("", "taskrunner-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Override logs directory
	oldLogsDir := agentserver.LogsDirectory
	agentserver.LogsDirectory = tempDir
	defer func() {
		agentserver.LogsDirectory = oldLogsDir
	}()

	os.Setenv("NAMESPACE", "default")
	os.Setenv("NAME", "test-sandbox")

	gvr := schema.GroupVersionResource{
		Group:    "custom.agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "sandboxtasks",
	}
	ao, _ := agentoutput.New(gvr, "default", "test-sandbox")

	tr := &TaskRunner{
		manager:     manager,
		namespace:   "default",
		sandboxName: "test-sandbox",
		ao:          ao,
	}

	// Define SandboxTask spec & status
	task := &sandboxtaskv1alpha1.SandboxTask{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "custom.agents.x-k8s.io/v1alpha1",
			Kind:       "SandboxTask",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-task-cancellation",
			Namespace: "default",
		},
		Spec: sandboxtaskv1alpha1.SandboxTaskSpec{
			SandboxName: "test-sandbox",
			Type:        "script",
			Params: map[string]string{
				"command": "sleep 10",
			},
		},
	}

	unstructuredMap, err := runtime.DefaultUnstructuredConverter.ToUnstructured(task)
	if err != nil {
		t.Fatalf("Failed to convert: %v", err)
	}

	_, err = dynamicClient.Resource(gvrSandboxTask).Namespace("default").Create(context.Background(), &unstructured.Unstructured{Object: unstructuredMap}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Failed to create SandboxTask: %v", err)
	}

	// Run executeTask in a goroutine
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Wait a moment, then set the SandboxTask state to "Cancelled" in the fake client
	go func() {
		time.Sleep(1 * time.Second)
		err := tr.manager.UpdateSandboxTaskStatus(ctx, "default", "test-task-cancellation", "Cancelled", "task cancelled", nil)
		if err != nil {
			t.Errorf("Failed to update status to Cancelled: %v", err)
		}
	}()

	tr.executeTask(ctx, task)

	// Fetch updated task from fake client and check status
	updatedUnstructured, err := dynamicClient.Resource(gvrSandboxTask).Namespace("default").Get(context.Background(), "test-task-cancellation", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Failed to get updated task: %v", err)
	}

	updatedTask := &sandboxtaskv1alpha1.SandboxTask{}
	_ = runtime.DefaultUnstructuredConverter.FromUnstructured(updatedUnstructured.UnstructuredContent(), updatedTask)

	if updatedTask.Status.TaskState != "Cancelled" {
		t.Errorf("Expected status.taskState to be 'Cancelled', got %s", updatedTask.Status.TaskState)
	}
}
