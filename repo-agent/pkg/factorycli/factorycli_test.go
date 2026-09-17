package factorycli

import "testing"

func TestExtractTriageYAML_RealFactoryOutput(t *testing.T) {
	output := "\n================= ISSUE TRIAGE =================\n" +
		"triage:\n  labels: [bug]\n" +
		"================================================\n"
	got := ExtractTriageYAML(output)
	if got != "triage:\n  labels: [bug]" {
		t.Fatalf("ExtractTriageYAML mismatch: %q", got)
	}
}

func TestDraftWasPosted(t *testing.T) {
	if !DraftWasPosted("...\nPosting review as a draft (pending) review to GitHub PR...\n") {
		t.Fatal("marker not detected")
	}
	if DraftWasPosted("================= CODE REVIEW =================") {
		t.Fatal("false positive")
	}
}
