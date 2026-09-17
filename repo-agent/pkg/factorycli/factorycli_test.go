package factorycli

import "testing"

// Real `factory pr review --publish no` output: one CODE REVIEW banner,
// closed by a plain '=' run (review.go prints no second banner). The
// original extractor demanded the banner twice and harvested nothing in
// production.
func TestExtractReviewYAML_RealFactoryOutput(t *testing.T) {
	output := "Reading output...\n" +
		"\n================= CODE REVIEW =================\n" +
		"review:\n  body: |\n    Looks good overall.\n" +
		"===============================================\n"
	got := ExtractReviewYAML(output)
	want := "review:\n  body: |\n    Looks good overall."
	if got != want {
		t.Fatalf("ExtractReviewYAML mismatch:\n got: %q\nwant: %q", got, want)
	}
}

func TestExtractReviewYAML_NoBanner(t *testing.T) {
	if got := ExtractReviewYAML("Posting review as a draft (pending) review to GitHub PR...\n"); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

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
