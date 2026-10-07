package factorycli

import (
	"slices"

	"gopkg.in/yaml.v3"
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

// What a verb needs of the viewer on the repository, to be offered
// enabled: nothing (anyone may comment on a public issue, or write a
// pending review), triage access, or push access.
const (
	NeedsNothing = ""
	NeedsTriage  = "triage"
	NeedsPush    = "push"
)

// verbNeeds are the verbs the board knows what they need. The draft verbs
// (edit, reject) and the follow-ups (run, revise) are the board's own;
// the rest are factory apply's writes, with the caller's token, to the
// target or the caller's fork. A verb not here needs push access.
var verbNeeds = map[string]string{
	"edit":         NeedsNothing,
	"reject":       NeedsNothing,
	"run":          NeedsNothing,
	"revise":       NeedsNothing,
	"comment":      NeedsNothing,
	"post-review":  NeedsNothing,
	"push-notes":   NeedsNothing,
	"open-pr":      NeedsNothing,
	"post-replies": NeedsNothing,
	"label":        NeedsTriage,
}

// VerbNeeds is what verb needs of the viewer on the repository.
func VerbNeeds(verb string) string {
	if needs, ok := verbNeeds[verb]; ok {
		return needs
	}
	return NeedsPush
}

// IsApplyVerb is whether verb is one factory apply executes: not a draft
// verb or a follow-up, which are the board's.
func IsApplyVerb(verb string) bool {
	switch verb {
	case "edit", "reject", "run", "revise":
		return false
	}
	return true
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
	"Notes": {
		{Verb: "edit", Field: "spec.markdown", Format: "markdown"},
		{Verb: "push-notes", Label: "Save to research/notes"},
		{Verb: "reject"},
	},
	"Review": {
		{Verb: "edit", Field: "spec", Format: "yaml"},
		{Verb: "post-review", Label: "Post as pending review"},
		{Verb: "reject"},
	},
	"Change": {
		{Verb: "edit", Field: "spec", Format: "yaml"},
		{Verb: "open-pr", Label: "Open draft PR"},
		{Verb: "post-replies", Label: "Post replies"},
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
	Preview string   `yaml:"preview"`
}

func parseTaskOutputMeta(kind, doc string) (taskOutputMeta, bool) {
	var m taskOutputMeta
	if doc == "" || yaml.Unmarshal([]byte(doc), &m) != nil || m.Kind != kind {
		return taskOutputMeta{}, false
	}
	return m, true
}

// OfferedActions are the actions a kind's task output offers: the
// document's, or the kind's defaults when it declares none or there is
// none. Whatever the kind, every verb is offered — what the viewer may
// take is VerbNeeds' — but for the follow-ups the board cannot start: a
// run other than fix, a revise with no id.
func OfferedActions(kind, doc string) []Action {
	actions := defaultActions[kind]
	if m, ok := parseTaskOutputMeta(kind, doc); ok && len(m.Actions) > 0 {
		actions = m.Actions
	}
	var out []Action
	for _, a := range actions {
		if a.Verb == "" || (a.Verb == "run" && a.Run != "fix") || (a.Verb == "revise" && a.Revise == "") {
			continue
		}
		out = append(out, a)
	}
	return out
}

// Offers is whether a kind's task output offers verb.
func Offers(kind, doc, verb string) bool {
	return slices.ContainsFunc(OfferedActions(kind, doc), func(a Action) bool { return a.Verb == verb })
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
