package envd

import (
	"bytes"
	"testing"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/geminitokens"
)

func TestQuotaEvidencePrefersSupersetBuffer(t *testing.T) {
	window := []byte("window carries history from previous polls")
	oversized := bytes.Repeat([]byte("x"), len(window)+1)
	tests := []struct {
		name   string
		chunk  []byte
		window []byte
		want   []byte
	}{
		{name: "empty chunk falls back to window", chunk: nil, window: window, want: window},
		{name: "ordinary chunk is contained in the window", chunk: []byte("tail"), window: window, want: window},
		{name: "oversized chunk is the superset", chunk: oversized, window: window, want: oversized},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := quotaEvidence(tc.chunk, tc.window); !bytes.Equal(got, tc.want) {
				t.Fatalf("quotaEvidence() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestModelExtractionSurvivesOversizedChunk reproduces the case where the model name is
// printed near the start of a poll chunk that is larger than the tracker's sliding window.
// The window only retains the trailing bytes, so the model must be recovered from the raw
// chunk - otherwise the key is marked quota-exceeded for every model instead of the one
// that actually failed.
func TestModelExtractionSurvivesOversizedChunk(t *testing.T) {
	tracker := geminitokens.NewQuotaStreamTracker()

	var chunk []byte
	chunk = append(chunk, []byte("Trying model: gemini-2.5-pro\n")...)
	chunk = append(chunk, bytes.Repeat([]byte("working on the task...\n"), 1000)...)
	chunk = append(chunk, []byte("Error: status: 429 Too Many Requests\n")...)
	if len(chunk) <= geminitokens.DefaultQuotaWindowSize {
		t.Fatalf("test setup: chunk must exceed the tracker window size")
	}

	tracker.ObservePoll(chunk)

	if model := extractModelFromLogs(tracker.Window()); model != "" {
		t.Fatalf("test setup: model unexpectedly still in the retained window (%q); the regression is not reproduced", model)
	}

	evidence := quotaEvidence(chunk, tracker.Window())
	if got, want := extractModelFromLogs(evidence), "gemini-2.5-pro"; got != want {
		t.Fatalf("extractModelFromLogs(evidence) = %q, want %q", got, want)
	}
}

func TestModelExtractionUsesWindowHistoryForSmallChunks(t *testing.T) {
	tracker := geminitokens.NewQuotaStreamTracker()
	tracker.ObservePoll([]byte("Trying model: gemini-2.5-flash\n"))

	// A later poll has no model line of its own; the window still carries it.
	newData := []byte("Error: status: 429 Too Many Requests\n")
	tracker.ObservePoll(newData)

	evidence := quotaEvidence(newData, tracker.Window())
	if got, want := extractModelFromLogs(evidence), "gemini-2.5-flash"; got != want {
		t.Fatalf("extractModelFromLogs(evidence) = %q, want %q", got, want)
	}
}

func TestFallbackLoopTracksFailedModelQuota(t *testing.T) {
	tracker := geminitokens.NewQuotaStreamTracker()

	// 1. Model 1 starts and encounters 429
	tracker.ObservePoll([]byte("Trying model: gemini-3.7-flash\n"))
	tracker.ObservePoll([]byte("Attempt 1 failed with status 429. Retrying with backoff... _ApiError: {\"error\":{\"message\":\"RESOURCE_EXHAUSTED\"}}\n"))

	// 2. Model 1 fails and switches to Model 2
	tracker.ObservePoll([]byte("Engine execution failed with model: gemini-3.7-flash. Retrying with next model...\nTrying model: gemini-3.6-flash\n"))

	newlyExceeded := tracker.NewlyExceededModels()
	if len(newlyExceeded) != 1 || newlyExceeded[0] != "gemini-3.7-flash" {
		t.Fatalf("expected [gemini-3.7-flash] to be newly marked as exceeded, got %v", newlyExceeded)
	}
	tracker.AckExceededModels(newlyExceeded...)

	// 3. Model 2 succeeds
	tracker.ObservePoll([]byte("Engine execution successful with model: gemini-3.6-flash\n"))

	if got := tracker.NewlyExceededModels(); len(got) != 0 {
		t.Fatalf("expected no new exceeded models, got %v", got)
	}

	allExceeded := tracker.ExceededModels()
	if len(allExceeded) != 1 || allExceeded[0] != "gemini-3.7-flash" {
		t.Fatalf("expected [gemini-3.7-flash] in ExceededModels, got %v", allExceeded)
	}
}

func TestModelExtractionWithSpecialCharacters(t *testing.T) {
	logs := []byte("Trying model: models/gemini-1.5-flash_preview\n")
	if got, want := extractModelFromLogs(logs), "models/gemini-1.5-flash_preview"; got != want {
		t.Fatalf("extractModelFromLogs(logs) = %q, want %q", got, want)
	}
}

func TestModelExtractionCaseInsensitive(t *testing.T) {
	logs := []byte("trying model: gemini-2.5-pro\n")
	if got, want := extractModelFromLogs(logs), "gemini-2.5-pro"; got != want {
		t.Fatalf("extractModelFromLogs(logs) = %q, want %q", got, want)
	}
}

func TestRecordExceededModelsAcknowledgesPendingModels(t *testing.T) {
	tracker := geminitokens.NewQuotaStreamTracker()
	tracker.ObservePoll([]byte("trying model: gemini-3.7-flash\n"))
	tracker.ObservePoll([]byte("status: 429 RESOURCE_EXHAUSTED\n"))
	tracker.ObservePoll([]byte("Engine execution failed with model: gemini-3.7-flash. Retrying with next model...\nTrying model: gemini-2.5-flash\n"))

	// Case 1: Key cannot be resolved -> models should still be acknowledged to prevent repeat polling loops,
	// and recordExceededModels returns the model that failed (gemini-3.7-flash)
	evidence := tracker.Window()
	model := recordExceededModels(evidence, tracker, nil)
	if model != "gemini-3.7-flash" {
		t.Fatalf("expected failed model gemini-3.7-flash, got %s", model)
	}
	if pending := tracker.NewlyExceededModels(); len(pending) != 0 {
		t.Fatalf("expected pending exceeded models to be cleared/acknowledged even when key is unresolved, got %v", pending)
	}

	// Case 2: Subsequent poll tick returns the last exceeded model (gemini-3.7-flash), avoiding falsely attributing to gemini-2.5-flash
	nextModel := recordExceededModels(evidence, tracker, nil)
	if nextModel != "gemini-3.7-flash" {
		t.Fatalf("expected last exceeded model gemini-3.7-flash, got %s", nextModel)
	}

	// Case 3: When no models have exceeded quota, recordExceededModels returns ""
	freshTracker := geminitokens.NewQuotaStreamTracker()
	freshTracker.ObservePoll([]byte("Trying model: gemini-2.5-pro\n"))
	freshTracker.ObservePoll([]byte("Working on task...\n"))
	if got := recordExceededModels(freshTracker.Window(), freshTracker, nil); got != "" {
		t.Fatalf("expected empty model string when no quota exceeded, got %s", got)
	}
}
