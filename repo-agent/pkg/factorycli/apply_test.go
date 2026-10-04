package factorycli

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The stored document keeps what the task said of itself — its actions,
// its source — and the draft, as edited, is the spec.
func TestComposeTaskOutput(t *testing.T) {
	header := withoutSpec(planTaskOutput)
	out, err := ComposeTaskOutput("Plan", header, "## Edited\n", "https://github.com/o/r/issues/1", "fallback")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Kind   string `yaml:"kind"`
		Source struct {
			Task string `yaml:"task"`
		} `yaml:"source"`
		Target struct {
			URL string `yaml:"url"`
		} `yaml:"target"`
		Spec    map[string]any `yaml:"spec"`
		Actions []Action       `yaml:"actions"`
	}
	if err := yaml.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	want, _ := parseTaskOutputMeta("Plan", planTaskOutput)
	if doc.Kind != "Plan" || doc.Source.Task != want.Source.Task || doc.Source.Task == "fallback" ||
		len(doc.Actions) != len(want.Actions) || doc.Spec["markdown"] != "## Edited" || doc.Target.URL == "" {
		t.Errorf("composed:\n%s", out)
	}

	// No stored document: a fresh one, named for the draft.
	out, err = ComposeTaskOutput("Triage", "", "triage:\n  labels: [bug]\n  duplicates: ['#3', 'nope', 4]\n  assessment: x", "https://github.com/o/r/issues/1", "board-sb-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"apiVersion: " + TaskOutputAPIVersion, "task: board-sb-1", "url: https://github.com/o/r/issues/1", "- 3\n", "- 4\n", "- bug"} {
		if !strings.Contains(out, s) {
			t.Errorf("missing %q in:\n%s", s, out)
		}
	}
	if strings.Contains(out, "nope") {
		t.Errorf("kept a duplicate that is not a number:\n%s", out)
	}

	if _, err := ComposeTaskOutput("Plan", "", "  ", "u", "t"); err == nil {
		t.Error("an empty plan composed")
	}
}
