package factorycli

import (
	"slices"

	"gopkg.in/yaml.v3"
)

// The task output documents a board keeps next to its drafts, for the
// actions they offer and the task that made them.
const (
	AnnotationTriageOutput = "board.gemini.google.com/triage-output"
	AnnotationPlanOutput   = "board.gemini.google.com/plan-output"

	// AnnotationTriageLabeled stamps a triage whose labels were added, and
	// AnnotationPlanCommented a plan that was posted: each a verb done.
	AnnotationTriageLabeled = "board.gemini.google.com/triage-labeled-at"
	AnnotationPlanCommented = "board.gemini.google.com/plan-commented-at"
)

// Action is one thing a task output offers to do with its result, as
// factory declares it (factory/pkg/taskoutput): a verb, what an edit
// edits, the follow-up a run starts, the recipe revise a revise runs, and
// a label for a button.
type Action struct {
	Verb   string `yaml:"verb" json:"verb"`
	Field  string `yaml:"field,omitempty" json:"field,omitempty"`
	Format string `yaml:"format,omitempty" json:"format,omitempty"`
	Run    string `yaml:"run,omitempty" json:"run,omitempty"`
	Revise string `yaml:"revise,omitempty" json:"revise,omitempty"`
	Label  string `yaml:"label,omitempty" json:"label,omitempty"`
}

// boardVerbs are the verbs the board executes, by kind; a run, only the
// fix follow-up. Anything else a document offers is not shown. Only a
// plan's revises so far: triage's are factory's phase 4.
var boardVerbs = map[string][]string{
	"Triage": {"edit", "label", "comment", "reject"},
	"Plan":   {"edit", "comment", "run", "revise", "reject"},
}

// defaultActions are a kind's actions when its document declares none (or
// there is no document: a draft stored before documents were kept), as
// factory's taskoutput.DefaultActions.
var defaultActions = map[string][]Action{
	"Triage": {
		{Verb: "edit", Field: "spec", Format: "yaml"},
		{Verb: "label"},
		{Verb: "comment"},
		{Verb: "reject"},
	},
	"Plan": {
		{Verb: "edit", Field: "spec.markdown", Format: "markdown"},
		{Verb: "comment"},
		{Verb: "run", Run: "fix", Label: "Fix with this plan"},
		{Verb: "reject"},
	},
}

// taskOutputMeta is what the board reads of a task output besides its
// spec.
type taskOutputMeta struct {
	Kind   string `yaml:"kind"`
	Source struct {
		Task    string `yaml:"task"`
		Session string `yaml:"session"`
	} `yaml:"source"`
	Actions []Action `yaml:"actions"`
}

func parseTaskOutputMeta(kind, doc string) (taskOutputMeta, bool) {
	var m taskOutputMeta
	if doc == "" || yaml.Unmarshal([]byte(doc), &m) != nil || m.Kind != kind {
		return taskOutputMeta{}, false
	}
	return m, true
}

// OfferedActions are the actions a kind's task output offers that the
// board executes: the document's, or the kind's defaults when it declares
// none or there is none.
func OfferedActions(kind, doc string) []Action {
	actions := defaultActions[kind]
	if m, ok := parseTaskOutputMeta(kind, doc); ok && len(m.Actions) > 0 {
		actions = m.Actions
	}
	var out []Action
	for _, a := range actions {
		if !slices.Contains(boardVerbs[kind], a.Verb) || (a.Verb == "run" && a.Run != "fix") || (a.Verb == "revise" && a.Revise == "") {
			continue
		}
		out = append(out, a)
	}
	return out
}

// TaskOutputTask is the task a kind's task output came from, or "": what
// factory marks the comments it posts with.
func TaskOutputTask(kind, doc string) string {
	m, _ := parseTaskOutputMeta(kind, doc)
	return m.Source.Task
}

// TaskOutputSession is the agent session a kind's task output came from,
// or "": its task's, or for a revise's output, the session it revised in.
// A revise of it revises in the same one.
func TaskOutputSession(kind, doc string) string {
	m, _ := parseTaskOutputMeta(kind, doc)
	if m.Source.Session != "" {
		return m.Source.Session
	}
	return m.Source.Task
}

// withoutSpec is a task output document without its spec, or "" for one
// that isn't a YAML mapping.
func withoutSpec(doc string) string {
	var m yaml.Node
	if yaml.Unmarshal([]byte(doc), &m) != nil || len(m.Content) != 1 || m.Content[0].Kind != yaml.MappingNode {
		return ""
	}
	root := m.Content[0]
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "spec" {
			root.Content = append(root.Content[:i], root.Content[i+2:]...)
			break
		}
	}
	out, err := yaml.Marshal(root)
	if err != nil {
		return ""
	}
	return string(out)
}
