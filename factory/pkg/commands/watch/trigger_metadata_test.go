package watch

import (
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/concurrency"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func newTestKubeClient() *clients.KubernetesClient {
	scheme := runtime.NewScheme()
	fakeDynamic := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		k8s.SandboxGVR: "SandboxList",
	})
	return &clients.KubernetesClient{
		DynamicClient: fakeDynamic,
	}
}

func TestBuildQueueResponseTriggerFields(t *testing.T) {
	tempDir := t.TempDir()
	mgr := concurrency.NewTaskQueueManager(concurrency.TaskQueueManagerConfig{
		QueueDir: tempDir,
	})

	eventTime := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	enqueuedTime := time.Date(2026, 8, 1, 10, 15, 0, 0, time.UTC)

	task := &api.QueueTask{
		Type:             api.TypePRComments,
		URL:              "https://github.com/owner/repo/pull/123",
		Number:           123,
		Priority:         api.PriorityMedium,
		Phase:            api.PhaseIterate,
		CreatedAt:        eventTime,
		EnqueuedAt:       enqueuedTime,
		TriggerEventTime: eventTime,
		TriggerReason:    api.TriggerReasonPRCommentsAdded,
		TriggerNotes:     "Oldest comment by alice added at 2026-08-01T10:00:00Z",
		Status:           api.StatusPending,
	}

	if err := mgr.Enqueue("task-pr-123-comments.yaml", task); err != nil {
		t.Fatalf("failed to enqueue task: %v", err)
	}

	resp := mgr.GetQueueResponse()
	if len(resp.Incoming) != 1 {
		t.Fatalf("expected 1 incoming task, got %d", len(resp.Incoming))
	}

	item := resp.Incoming[0]
	if item.TriggerEventTime != eventTime.Format(time.RFC3339) {
		t.Errorf("expected item TriggerEventTime %s, got %s", eventTime.Format(time.RFC3339), item.TriggerEventTime)
	}
	if item.TriggerReason != api.TriggerReasonPRCommentsAdded {
		t.Errorf("expected item TriggerReason '%s', got '%s'", api.TriggerReasonPRCommentsAdded, item.TriggerReason)
	}
	if item.TriggerNotes != "Oldest comment by alice added at 2026-08-01T10:00:00Z" {
		t.Errorf("expected item TriggerNotes 'Oldest comment by alice added at 2026-08-01T10:00:00Z', got '%s'", item.TriggerNotes)
	}
}
