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
