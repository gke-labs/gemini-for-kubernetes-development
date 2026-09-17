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
