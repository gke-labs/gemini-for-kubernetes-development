package factorycli

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The stored document keeps what the task said of itself — its actions,
// its source — and the draft, as edited, is the spec.
func TestComposeTaskOutput(t *testing.T) {
	out, err := ComposeTaskOutput("Plan", planTaskOutput, "## Edited\n", "https://github.com/o/r/issues/1", "fallback")
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

// A Notes revise's document is read out of the harvest as a plan's is,
// and stored whole; a plan is not notes.
func TestNotesTaskOutput(t *testing.T) {
	doc := `apiVersion: ` + TaskOutputAPIVersion + `
kind: Notes
source:
  task: recipe-revise-notes-1
spec:
  markdown: |
    # Findings
actions:
  - verb: push-notes
    label: Save to research/notes
`
	out := "Revising...\n" + planBanner + "\n" + doc + bannerCloser + "\n"
	if got := ExtractNotes(out); got != "# Findings" {
		t.Errorf("ExtractNotes = %q", got)
	}
	header := NotesTaskOutput(out)
	if Draft("Notes", header) != "# Findings" {
		t.Fatalf("NotesTaskOutput = %q, want the whole document", header)
	}
	if acts := OfferedActions("Notes", header); len(acts) != 1 || acts[0].Verb != "push-notes" {
		t.Errorf("offered = %+v", acts)
	}
	if ExtractNotes(planBanner+"\n"+planTaskOutput+bannerCloser+"\n") != "" {
		t.Error("a plan read as notes")
	}

	// The stored document is kept, edits included; the name and the
	// repository are set as it is applied.
	stored, err := WithDraft("Notes", header, "# Edited\n")
	if err != nil {
		t.Fatal(err)
	}
	composed, err := ApplyDoc(stored, "board-sb-1", "https://github.com/o/r", map[string]string{"name": "my-notes"})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Kind   string `yaml:"kind"`
		Source struct {
			Task string `yaml:"task"`
		} `yaml:"source"`
		Target struct {
			URL string `yaml:"url"`
		} `yaml:"target"`
		Spec map[string]any `yaml:"spec"`
	}
	if err := yaml.Unmarshal([]byte(composed), &got); err != nil {
		t.Fatalf("%v\n%s", err, composed)
	}
	if got.Kind != "Notes" || got.Source.Task != "recipe-revise-notes-1" || got.Target.URL != "https://github.com/o/r" ||
		got.Spec["markdown"] != "# Edited" || got.Spec["name"] != "my-notes" {
		t.Errorf("composed:\n%s", composed)
	}
	if _, err := ApplyDoc("", "t", "u", nil); err == nil {
		t.Error("no document applied")
	}
	if acts := OfferedActions("Notes", ""); len(acts) != 3 {
		t.Errorf("default Notes actions = %+v", acts)
	}
}
