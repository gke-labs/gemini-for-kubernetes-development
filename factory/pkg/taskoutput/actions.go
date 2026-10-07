package taskoutput

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Action is one thing that can be done with a result. A recipe declares
// the actions its result offers (task-output: actions:) and the runner
// copies them into the document, so that whoever shows the result builds
// its controls from the document rather than from its kind. An action
// names a verb of the registry; it never carries code, a command or a
// URL, so a document — written next to an agent that read untrusted
// text — can only narrow what is offered, never widen it.
type Action struct {
	Verb string `yaml:"verb" json:"verb"`
	// Field and Format say what an edit edits: spec.markdown as markdown,
	// or spec as YAML.
	Field  string `yaml:"field,omitempty" json:"field,omitempty"`
	Format string `yaml:"format,omitempty" json:"format,omitempty"`
	// Run is the follow-up a run starts, with this result as its input.
	Run string `yaml:"run,omitempty" json:"run,omitempty"`
	// Revise is the recipe's revise a revise action runs, into the
	// conversation the result came from.
	Revise string `yaml:"revise,omitempty" json:"revise,omitempty"`
	// Label is what to call it, for a button.
	Label string `yaml:"label,omitempty" json:"label,omitempty"`
}

// arg is what the action's verb acts with: the follow-up a run starts,
// the revise a revise runs.
func (a Action) arg() string {
	if a.Verb == "revise" {
		return a.Revise
	}
	return a.Run
}

func (a Action) String() string {
	return strings.TrimSuffix(a.Verb+" "+a.arg(), " ")
}

// Class is who executes a verb.
type Class string

const (
	// ClassApply writes to GitHub with the caller's token — to the
	// document's target, or for push-notes to the caller's fork of it: factory
	// apply --action.
	ClassApply Class = "apply"
	// ClassFollowUp starts another task with the result as its input:
	// factory apply --action run.
	ClassFollowUp Class = "follow-up"
	// ClassDraft is the business of whoever keeps the result as a draft:
	// factory executes none. From the CLI, editing is editing the file
	// before apply -f, and rejecting is not applying.
	ClassDraft Class = "draft"
)

// verb is what the registry knows of one verb.
type verb struct {
	class Class
	kinds []string
	// apply is an apply verb's write.
	apply applyFunc
}

var verbs = map[string]verb{
	"comment":      {class: ClassApply, kinds: []string{"Triage", "Plan", "Summary"}, apply: applyComment},
	"label":        {class: ClassApply, kinds: []string{"Triage"}, apply: applyLabels},
	"push-notes":   {class: ClassApply, kinds: []string{"Notes"}, apply: applyPushNotes},
	"post-review":  {class: ClassApply, kinds: []string{"Review"}, apply: applyPostReview},
	"open-pr":      {class: ClassApply, kinds: []string{"Change"}, apply: applyOpenPR},
	"post-replies": {class: ClassApply, kinds: []string{"Change"}, apply: applyPostReplies},
	"run":          {class: ClassFollowUp, kinds: []string{"Triage", "Plan", "Notes", "Review", "Change", "Summary"}},
	"revise":       {class: ClassFollowUp, kinds: []string{"Triage", "Plan", "Notes", "Review", "Change", "Summary"}},
	"edit":         {class: ClassDraft, kinds: []string{"Triage", "Plan", "Notes", "Review", "Change", "Summary"}},
	"reject":       {class: ClassDraft, kinds: []string{"Triage", "Plan", "Notes", "Review", "Change", "Summary"}},
}

// defaultActions are what a kind's result offers when its document
// declares none: one from a runner or recipe older than actions, or
// written by hand.
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
	"Summary": {
		{Verb: "edit", Field: "spec.markdown", Format: "markdown"},
		{Verb: "comment"},
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

var (
	editFieldRE = regexp.MustCompile(`^spec(\.[a-z][A-Za-z0-9]*)?$`)
	reviseRE    = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
)

// VerbClass is verb's class, and false for a verb the registry doesn't
// have.
func VerbClass(name string) (Class, bool) {
	v, ok := verbs[name]
	return v.class, ok
}

// KnownVerbs lists the registry's verbs, for error messages.
func KnownVerbs() []string {
	var out []string
	for v := range verbs {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// DefaultActions are what a kind's result offers without a declaration.
func DefaultActions(kind string) []Action {
	return slices.Clone(defaultActions[kind])
}

// ValidateActions checks a recipe's declaration for kind: every verb in
// the registry and accepting kind, at most once each (a run once per
// follow-up), with what that verb needs.
func ValidateActions(kind string, actions []Action) error {
	seen := map[string]bool{}
	for _, a := range actions {
		v, ok := verbs[a.Verb]
		if !ok {
			return fmt.Errorf("action %q is not one of: %s", a.Verb, strings.Join(KnownVerbs(), ", "))
		}
		if !slices.Contains(v.kinds, kind) {
			return fmt.Errorf("action %s does not take a %s", a.Verb, kind)
		}
		key := a.Verb + "/" + a.arg()
		if seen[key] {
			return fmt.Errorf("action %s declared twice", a)
		}
		seen[key] = true
		switch a.Verb {
		case "edit":
			if !editFieldRE.MatchString(a.Field) {
				return fmt.Errorf("action edit: field %q must be spec or spec.<field>", a.Field)
			}
			if a.Format != "yaml" && a.Format != "markdown" {
				return fmt.Errorf("action edit: format %q must be yaml or markdown", a.Format)
			}
		case "run":
			// Whether the recipe takes the result is the recipe's to say
			// (an input from: kind), checked where recipes are.
			if !reviseRE.MatchString(a.Run) {
				return fmt.Errorf("action run: recipe %q must match %s", a.Run, reviseRE)
			}
			if a.Field != "" || a.Format != "" || a.Revise != "" {
				return fmt.Errorf("action run takes only a run and a label")
			}
		case "revise":
			if !reviseRE.MatchString(a.Revise) {
				return fmt.Errorf("action revise: revise %q must match %s", a.Revise, reviseRE)
			}
			if a.Field != "" || a.Format != "" || a.Run != "" {
				return fmt.Errorf("action revise takes only a revise and a label")
			}
		default:
			if a.Field != "" || a.Format != "" || a.Run != "" || a.Revise != "" {
				return fmt.Errorf("action %s takes only a label", a.Verb)
			}
		}
	}
	return nil
}

// Offered is what the document offers: its actions, or its kind's
// defaults when it declares none. A verb this factory doesn't know, from
// a newer one, is left out, as a kind it doesn't know is.
func (d *Document) Offered() []Action {
	actions := d.Actions
	if len(actions) == 0 {
		actions = defaultActions[d.Kind]
	}
	var out []Action
	for _, a := range actions {
		if v, ok := verbs[a.Verb]; ok && slices.Contains(v.kinds, d.Kind) {
			out = append(out, a)
		}
	}
	return out
}

// Offer is the action the document offers for verb (and arg: for a run,
// the follow-up; for a revise, the revise), or an error saying what it
// offers instead.
func (d *Document) Offer(verbName, arg string) (Action, error) {
	var offered []string
	for _, a := range d.Offered() {
		if a.Verb == verbName && (arg == "" || a.arg() == arg) {
			return a, nil
		}
		offered = append(offered, a.String())
	}
	if arg != "" {
		verbName += " " + arg
	}
	return Action{}, fmt.Errorf("the %s does not offer %s; it offers: %s", d.Kind, verbName, strings.Join(offered, ", "))
}
