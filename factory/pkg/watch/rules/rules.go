// Package rules is the model of the watch loop's rules
// (factory/design/watch-rules.md): a GitHub condition and the action taken
// on it, one rule per file. The prebaked rules, factory's own, are embedded
// here, one file each. Set.Decide evaluates them for one item from facts
// its caller gathered, and acts on nothing: the watch's scanners gather the
// facts and carry out what it decides; a Summary tallies the decisions, and
// the record replays them.
//
// Fan-out, the warm workspace and the sandbox GC are not rules: they stay
// plugins of the watch, as in the design.
package rules

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/recipe"
)

// What a rule looks at.
const (
	// OnIssue and OnPR are an issue's and a pull request's rules.
	// Scheduled chores (the chore scheduler, switched by chores.mode) and
	// the linked-issue nudge (a workflow stepped when an issue it links
	// closes, decided again at start as the workflow rule) are code, not
	// rules.
	OnIssue = "issue"
	OnPR    = "pr"
)

// Floors a rule's policy cannot go past: a repository's rule that drops its
// give-up or its cooldown would loop (design: Compared to today).
const (
	MaxAttempts = 10
	MinCooldown = time.Minute
)

// Rule is one rule file.
type Rule struct {
	// Name names the rule; a repository's rule of the same name replaces
	// factory's (Phase 5).
	Name string `yaml:"name" json:"name"`
	// Description says what the rule is for, in a line.
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	// On is what the rule looks at: OnIssue or OnPR.
	On string `yaml:"on" json:"on"`
	// Enabled turns the rule off when false; unset is on. A rule's own
	// file sets it, and a repository's file of the same name with only a
	// name and enabled turns factory's rule on or off (IsToggle, Overlay).
	Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	// Order is the rule's place among the rules on its kind of item,
	// lower first: one ordered list per kind (Set.Ordered). Unset, the
	// rule goes after the ordered ones; a rule that takes the item needs
	// one.
	Order int `yaml:"order,omitempty" json:"order,omitempty"`
	// Exclusive is whether the rule takes the item: of the rules that do,
	// the first in order that holds is the only one decided, and none is
	// while a task of the item is active. Unset, a recipe rule takes the
	// item and a rule of GitHub writes does not (IsExclusive).
	Exclusive *bool `yaml:"exclusive,omitempty" json:"exclusive,omitempty"`
	// When is the condition: every predicate set must hold.
	When When `yaml:"when" json:"when"`
	// Run is the action.
	Run Run `yaml:"run" json:"run"`
	// Policy is the rule's dedupe, cooldown, give-up and ordering.
	Policy Policy `yaml:"policy,omitempty" json:"policy,omitzero"`

	// Source is whose rule it is, SourceFactory or SourceRepo, and
	// EnabledBy whose its Enabled is when a repository's toggle set it;
	// neither is written in a rule file.
	Source    string `yaml:"-" json:"source,omitempty"`
	EnabledBy string `yaml:"-" json:"enabledBy,omitempty"`
}

// IsEnabled reports whether the rule is on.
func (r *Rule) IsEnabled() bool { return r.Enabled == nil || *r.Enabled }

// IsExclusive reports whether the rule takes the item: as its file says,
// else a recipe rule does and a rule of GitHub writes does not.
func (r *Rule) IsExclusive() bool {
	if r.Exclusive != nil {
		return *r.Exclusive
	}
	return r.Run.Recipe != ""
}

// Labels matches an item's labels: any of Any, all of All, none of None.
// A label may name the trigger ({{trigger}}) or a glob of a prefix
// ("*/review").
type Labels struct {
	Any  []string `yaml:"any,omitempty" json:"any,omitempty"`
	All  []string `yaml:"all,omitempty" json:"all,omitempty"`
	None []string `yaml:"none,omitempty" json:"none,omitempty"`
}

// Since is what resets a count: the latest of its events (head: the PR's
// head changed; human: a trusted human commented; pause: the watcher
// last put the item on hold). One event or a list of them; none counts every run.
type Since []string

// UnmarshalYAML reads one event or a list.
func (s *Since) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		var one string
		if err := n.Decode(&one); err != nil {
			return err
		}
		*s = nil
		if one != "" {
			*s = Since{one}
		}
		return nil
	}
	var many []string
	if err := n.Decode(&many); err != nil {
		return err
	}
	*s = many
	return nil
}

// MarshalYAML writes one event as a scalar, more as a list.
func (s Since) MarshalYAML() (interface{}, error) {
	if len(s) == 1 {
		return s[0], nil
	}
	return []string(s), nil
}

// String is the events, joined with +.
func (s Since) String() string { return strings.Join(s, "+") }

// Closes matches the issues a PR closes: Labels against the labels they
// carry between them.
type Closes struct {
	Labels *Labels `yaml:"labels,omitempty" json:"labels,omitempty"`
}

// When is the closed set of predicates (design: Predicates). A nil or empty
// field is not checked; every field set must hold.
type When struct {
	// AnyOf holds when one of its predicate sets does, besides the rest:
	// an item may come to a rule several ways (an issue labelled, assigned
	// to the pool, or opened by the watcher). One level: a set in AnyOf
	// has no AnyOf of its own.
	AnyOf []When `yaml:"any-of,omitempty" json:"anyOf,omitempty"`

	// Any item.
	Labels   *Labels `yaml:"labels,omitempty" json:"labels,omitempty"`
	Assignee string  `yaml:"assignee,omitempty" json:"assignee,omitempty"` // pool | not-pool | none | owner
	Author   Authors `yaml:"author,omitempty" json:"author,omitempty"`     // pool | owner | member | collaborator | external: one or a list (AuthorKind)
	// LastRun is the outcome of the rule's last run on the item, and on a
	// PR on its current head: failed.
	LastRun string `yaml:"last-run,omitempty" json:"last-run,omitempty"`

	// Issues.
	LinkedPR string `yaml:"linked-pr,omitempty" json:"linked-pr,omitempty"` // none | open: a PR closing it
	// ConnectedPR is an open PR connected to the issue on its timeline: a
	// read of its own, made only for a rule that asks (Set.Needs).
	ConnectedPR string `yaml:"connected-pr,omitempty" json:"connected-pr,omitempty"` // none | open
	Workflow    string `yaml:"workflow,omitempty" json:"workflow,omitempty"`         // none | any (one it can load) | missing (one it cannot)
	OpenedBy    string `yaml:"opened-by,omitempty" json:"opened-by,omitempty"`       // watcher
	// Fanout is whether the issue is a fan-out's parent (it carries the
	// fanout label): the fan-out controller's, never fixed itself. Its
	// children are ordinary issues, created when they are to be worked on.
	Fanout *bool `yaml:"fanout,omitempty" json:"fanout,omitempty"`
	// Graph is whether the issue is an issue graph's parent (it carries
	// the graph label): the graph controller's, never fixed itself. Its
	// children are ordinary issues, created when their steps are ready
	// (design/issue-graph.md).
	Graph *bool `yaml:"graph,omitempty" json:"graph,omitempty"`

	// Pull requests.
	Checks    string `yaml:"checks,omitempty" json:"checks,omitempty"` // failing | pending | green
	Mergeable *bool  `yaml:"mergeable,omitempty" json:"mergeable,omitempty"`
	Feedback  string `yaml:"feedback,omitempty" json:"feedback,omitempty"` // new-trusted | none
	// Closes matches the issues the PR closes, between them.
	Closes         *Closes `yaml:"closes,omitempty" json:"closes,omitempty"`
	ReviewedAtHead *bool   `yaml:"reviewed-at-head,omitempty" json:"reviewed-at-head,omitempty"`
	// ChangesRequested is whether the automated review of the head asked
	// for changes.
	ChangesRequested *bool    `yaml:"changes-requested,omitempty" json:"changes-requested,omitempty"`
	Approved         *bool    `yaml:"approved,omitempty" json:"approved,omitempty"`
	Draft            *bool    `yaml:"draft,omitempty" json:"draft,omitempty"`
	InactiveFor      Duration `yaml:"inactive-for,omitempty" json:"inactive-for,omitzero"`
}

// Run is the action: a recipe run, or GitHub writes.
type Run struct {
	// Recipe is a factory recipe to run on the item, Apply the action its
	// result is applied with when the run ends, Inputs its inputs.
	Recipe string            `yaml:"recipe,omitempty" json:"recipe,omitempty"`
	Apply  string            `yaml:"apply,omitempty" json:"apply,omitempty"`
	Inputs map[string]string `yaml:"inputs,omitempty" json:"inputs,omitempty"`

	// Acknowledge feedback (PRs) marks the feedback the recipe's task
	// answers as seen when it starts, and resolved or failed when it ends,
	// made by the watcher.
	Acknowledge string `yaml:"acknowledge,omitempty" json:"acknowledge,omitempty"`
	// The lifecycle hooks: the watcher's writes at points of the recipe's
	// task's life. OnStart when it starts ({{bot}} is the bot it runs as:
	// assign it and unassign {{watcher}} to claim the item); OnSuccess when it completed (its apply included);
	// OnFailure when it failed (a step, its result or its apply) - the only
	// hook that may stop the item; OnEnd either way, after OnSuccess or
	// OnFailure. A task dropped before it starts runs none; one a restart
	// interrupts ends when it really does. A rule whose trigger is a label
	// it takes off when done is a command: applying the label again runs it
	// again.
	OnStart   *Hook `yaml:"on-start,omitempty" json:"on-start,omitempty"`
	OnSuccess *Hook `yaml:"on-success,omitempty" json:"on-success,omitempty"`
	OnFailure *Hook `yaml:"on-failure,omitempty" json:"on-failure,omitempty"`
	OnEnd     *Hook `yaml:"on-end,omitempty" json:"on-end,omitempty"`

	// GitHub writes, done by the watcher.
	Label    []string `yaml:"label,omitempty" json:"label,omitempty"`
	Unlabel  []string `yaml:"unlabel,omitempty" json:"unlabel,omitempty"`
	Comment  string   `yaml:"comment,omitempty" json:"comment,omitempty"`
	Assign   string   `yaml:"assign,omitempty" json:"assign,omitempty"`
	Unassign string   `yaml:"unassign,omitempty" json:"unassign,omitempty"`
	// Stop adds the stop label, with a comment saying why.
	Stop *Stop `yaml:"stop,omitempty" json:"stop,omitempty"`
	// Drop drops the item's queued tasks, from the queue and again at
	// dispatch.
	Drop bool `yaml:"drop,omitempty" json:"drop,omitempty"`
}

// Hook is GitHub writes the watcher makes at a point of a recipe task's
// life (Run.OnStart, OnSuccess, OnFailure, OnEnd): a rule's writes but
// drop; stop only on failure.
type Hook struct {
	Label    []string `yaml:"label,omitempty" json:"label,omitempty"`
	Unlabel  []string `yaml:"unlabel,omitempty" json:"unlabel,omitempty"`
	Comment  string   `yaml:"comment,omitempty" json:"comment,omitempty"`
	Assign   string   `yaml:"assign,omitempty" json:"assign,omitempty"`
	Unassign string   `yaml:"unassign,omitempty" json:"unassign,omitempty"`
	Stop     *Stop    `yaml:"stop,omitempty" json:"stop,omitempty"`
}

// run is the hook as a Run's writes, for resolveWrites.
func (h *Hook) run() Run {
	if h == nil {
		return Run{}
	}
	return Run{Label: h.Label, Unlabel: h.Unlabel, Comment: h.Comment, Assign: h.Assign, Unassign: h.Unassign, Stop: h.Stop}
}

// hooks are the run's hooks by name, as a rule file writes them.
func (r Run) hooks() map[string]*Hook {
	return map[string]*Hook{"on-start": r.OnStart, "on-success": r.OnSuccess, "on-failure": r.OnFailure, "on-end": r.OnEnd}
}

// Stop is the give-up action: the stop label and a comment.
type Stop struct {
	Comment string `yaml:"comment,omitempty" json:"comment,omitempty"`
}

func (r Run) writes() bool {
	return len(r.Label) > 0 || len(r.Unlabel) > 0 || r.Comment != "" || r.Assign != "" || r.Unassign != "" || r.Stop != nil || r.Drop
}

// Policy is a rule's dedupe, cooldown, give-up and ordering. A rule
// whose task for the item is queued or running decides nothing until it
// ends, whatever its policy.
type Policy struct {
	// Dedupe narrows that further: head (once per PR head) or update
	// (once per change to the item); a failed run does not count.
	Dedupe   string    `yaml:"dedupe,omitempty" json:"dedupe,omitempty"`
	Cooldown *Cooldown `yaml:"cooldown,omitempty" json:"cooldown,omitempty"`
	GiveUp   *GiveUp   `yaml:"give-up,omitempty" json:"give-up,omitempty"`
	// Phase orders the rule's tasks after the priority label: lower first.
	Phase int `yaml:"phase,omitempty" json:"phase,omitempty"`
	// Revisit every-scan looks at the item again every scan once the rule
	// is decided, instead of skipping it until it changes: what it waits
	// on (a PR's conflict resolved by a change to its base) need not
	// change the item itself.
	Revisit string `yaml:"revisit,omitempty" json:"revisit,omitempty"`
}

// Cooldown is how long after a run, by its outcome, the rule waits before
// it runs on the item again.
type Cooldown struct {
	Success Duration `yaml:"success,omitempty" json:"success,omitzero"`
	Failure Duration `yaml:"failure,omitempty" json:"failure,omitzero"`
	// ResetOn ends the cooldown early: head, when the PR's head changes.
	ResetOn []string `yaml:"reset-on,omitempty" json:"reset-on,omitempty"`
	// SkipWhen is an item the cooldown does not hold back (a PR assigned
	// to the pool explicitly): assignee only.
	SkipWhen *When `yaml:"skip-when,omitempty" json:"skip-when,omitempty"`
}

// GiveUp stops the rule on an item after Attempts runs since Since: the
// stop label, and Comment saying why.
type GiveUp struct {
	Attempts int   `yaml:"attempts" json:"attempts"`
	Since    Since `yaml:"since,omitempty" json:"since,omitempty"`
	// Failed counts only the runs that failed: a rule that runs again on
	// news (comments, on new feedback) gives up on failing, not on being
	// asked.
	Failed  bool   `yaml:"failed,omitempty" json:"failed,omitempty"`
	Comment string `yaml:"comment,omitempty" json:"comment,omitempty"`
}

// Duration is a time.Duration written as Go writes one ("10m", "2h"), or
// a reference to one the engine reads ({{pr-inactivity-timeout}}: the
// overseer's setting; {{workflow-cooldown}}: the workflow definition's).
type Duration struct {
	time.Duration
	Ref string
}

// durationRefs are the durations a rule may name rather than state.
var durationRefs = []string{"{{pr-inactivity-timeout}}", "{{workflow-cooldown}}"}

// IsSet is whether the duration is stated or named.
func (d Duration) IsSet() bool { return d.Duration != 0 || d.Ref != "" }

// UnmarshalYAML reads a duration string, or a reference.
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	d.Duration, d.Ref = 0, ""
	if s == "" {
		return nil
	}
	if strings.HasPrefix(s, "{{") {
		if !slices.Contains(durationRefs, s) {
			return fmt.Errorf("line %d: %q is not a duration factory reads (%s)", n.Line, s, strings.Join(durationRefs, ", "))
		}
		d.Ref = s
		return nil
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("line %d: %q is not a duration (e.g. 10m, 2h): %w", n.Line, s, err)
	}
	d.Duration = v
	return nil
}

// String writes 10m and 2h, not 10m0s and 2h0m0s, or the reference.
func (d Duration) String() string {
	if d.Ref != "" {
		return d.Ref
	}
	s := d.Duration.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

// MarshalYAML writes the duration string, nothing when zero.
func (d Duration) MarshalYAML() (interface{}, error) {
	if !d.IsSet() {
		return nil, nil
	}
	return d.String(), nil
}

// IsZero lets omitempty leave a zero duration out.
func (d Duration) IsZero() bool { return !d.IsSet() }

// MarshalJSON writes the duration string.
func (d Duration) MarshalJSON() ([]byte, error) {
	return []byte(fmt.Sprintf("%q", d.String())), nil
}

// Parse reads one rule file, strictly: an unknown field is an error.
func Parse(data []byte) (*Rule, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var r Rule
	if err := dec.Decode(&r); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("empty rule file")
		}
		return nil, err
	}
	return &r, nil
}

// predicates are the When fields each kind of rule may set, by YAML name.
var predicates = map[string][]string{
	"*":     {"any-of", "labels", "assignee", "author", "last-run"},
	OnIssue: {"linked-pr", "connected-pr", "workflow", "opened-by", "fanout", "graph"},
	OnPR:    {"checks", "mergeable", "feedback", "closes", "reviewed-at-head", "changes-requested", "approved", "draft", "inactive-for"},
}

// set lists the When fields that are set, by YAML name.
func (w When) set() []string {
	var out []string
	add := func(name string, ok bool) {
		if ok {
			out = append(out, name)
		}
	}
	add("any-of", len(w.AnyOf) > 0)
	add("labels", w.Labels != nil)
	add("assignee", w.Assignee != "")
	add("author", len(w.Author) > 0)
	add("last-run", w.LastRun != "")
	add("linked-pr", w.LinkedPR != "")
	add("connected-pr", w.ConnectedPR != "")
	add("workflow", w.Workflow != "")
	add("opened-by", w.OpenedBy != "")
	add("fanout", w.Fanout != nil)
	add("graph", w.Graph != nil)
	add("checks", w.Checks != "")
	add("mergeable", w.Mergeable != nil)
	add("feedback", w.Feedback != "")
	add("closes", w.Closes != nil)
	add("reviewed-at-head", w.ReviewedAtHead != nil)
	add("changes-requested", w.ChangesRequested != nil)
	add("approved", w.Approved != nil)
	add("draft", w.Draft != nil)
	add("inactive-for", w.InactiveFor.IsSet())
	return out
}

// enums are the values a string predicate or field may take.
var enums = map[string][]string{
	"assignee":     {"pool", "not-pool", "none"},
	"author":       {AuthorPool, AuthorOwner, AuthorMember, AuthorCollaborator, AuthorExternal},
	"linked-pr":    {"none", "open"},
	"connected-pr": {"none", "open"},
	"workflow":     {"none", "any", "missing"},
	"opened-by":    {"watcher"},
	"checks":       {"failing", "pending", "green"},
	"feedback":     {"new-trusted", "none"},
	"last-run":     {"failed"},
	"reset-on":     {"head"},
	"dedupe":       {"head", "update"},
	"revisit":      {"every-scan"},
	"acknowledge":  {AcknowledgeFeedback},
	"since":        {"head", "human", "pause"},
}

func checkEnum(field, value string) error {
	if value == "" || slices.Contains(enums[field], value) {
		return nil
	}
	return fmt.Errorf("%s: %q is not one of %s", field, value, strings.Join(enums[field], ", "))
}

func checkSince(s Since) error {
	for _, e := range s {
		if err := checkEnum("since", e); err != nil {
			return err
		}
	}
	return nil
}

// validate checks a predicate set: each predicate one a rule on on may
// set, its value one of the predicate's, and any-of one level deep. Errors
// start with the field's path, for the caller to prefix.
func (w When) validate(on string, nested bool) error {
	allowed := append(slices.Clone(predicates["*"]), predicates[on]...)
	for _, p := range w.set() {
		if !slices.Contains(allowed, p) {
			return fmt.Errorf(".%s is not a predicate of a rule on %s", p, on)
		}
	}
	for field, value := range map[string]string{
		"assignee": w.Assignee, "linked-pr": w.LinkedPR, "connected-pr": w.ConnectedPR,
		"workflow": w.Workflow, "opened-by": w.OpenedBy,
		"checks": w.Checks, "feedback": w.Feedback, "last-run": w.LastRun,
	} {
		if err := checkEnum(field, value); err != nil {
			return fmt.Errorf(".%v", err)
		}
	}
	for i, a := range w.Author {
		if err := checkEnum("author", a); err != nil {
			return fmt.Errorf(".%v", err)
		}
		if slices.Contains(w.Author[:i], a) {
			return fmt.Errorf(".author: %s is given twice", a)
		}
	}
	if c := w.Closes; c != nil && c.Labels == nil {
		return errors.New(".closes needs labels")
	}
	if len(w.AnyOf) > 0 {
		if nested {
			return errors.New(".any-of: a set in any-of has no any-of of its own")
		}
		if len(w.AnyOf) < 2 {
			return errors.New(".any-of: give two sets or more (one is just its predicates)")
		}
		for i, alt := range w.AnyOf {
			if len(alt.set()) == 0 {
				return fmt.Errorf(".any-of[%d] is empty", i)
			}
			if err := alt.validate(on, true); err != nil {
				return fmt.Errorf(".any-of[%d]%v", i, err)
			}
		}
	}
	return nil
}

// placeholderRe finds a {{name}} in a rule's strings.
var placeholderRe = regexp.MustCompile(`\{\{[^}]*\}\}`)

// recipeNameRE is a recipe's name, as a repository's .agents/recipes file
// is named, and a rule's.
var recipeNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// Placeholders are the names the engine fills in, by where a rule may use
// them: labels (in when and in the label writes), assignees (assign and
// unassign) and recipe inputs.
var (
	labelPlaceholders = map[string][]string{
		"*":  {"{{trigger}}"},
		OnPR: {"{{closing-issue-labels}}"},
	}
	assigneePlaceholders = map[string][]string{
		"*":  {"{{watcher}}", "{{pool}}"},
		OnPR: {"{{closing-issue-assignees}}"},
	}
	inputPlaceholders = map[string][]string{
		"*":     {"{{trigger}}"},
		OnIssue: {"{{pr-labels}}"},
		OnPR:    {ReviewInstructions},
	}
)

// AcknowledgeFeedback is run.acknowledge's one value: the feedback a PR's
// task answers.
const AcknowledgeFeedback = "feedback"

// ReviewInstructions is the input placeholder for the review instructions
// the pull request and the issues it closes carry: the scanner, which has
// read them, gives them to the recipe as its --instruction flags.
const ReviewInstructions = "{{review-instructions}}"

// Asks reports whether the rule's run gives one of its inputs as
// placeholder, whole.
func (r *Run) Asks(placeholder string) bool {
	for _, v := range r.Inputs {
		if strings.TrimSpace(v) == placeholder {
			return true
		}
	}
	return false
}

// checkPlaceholders checks every {{name}} the rule uses is one the engine
// fills in where it is used.
func (r *Rule) checkPlaceholders() error {
	check := func(where string, vocab map[string][]string, values ...string) error {
		ok := append(slices.Clone(vocab["*"]), vocab[r.On]...)
		for _, v := range values {
			for _, p := range placeholderRe.FindAllString(v, -1) {
				if !slices.Contains(ok, p) {
					return fmt.Errorf("%s: %s is not one of %s", where, p, strings.Join(ok, ", "))
				}
			}
		}
		return nil
	}
	var labels []string
	addLabels := func(l *Labels) {
		if l != nil {
			labels = append(labels, l.Any...)
			labels = append(labels, l.All...)
			labels = append(labels, l.None...)
		}
	}
	for _, w := range append([]When{r.When}, r.When.AnyOf...) {
		addLabels(w.Labels)
		if w.Closes != nil {
			addLabels(w.Closes.Labels)
		}
	}
	if err := check("when labels", map[string][]string{"*": labelPlaceholders["*"]}, labels...); err != nil {
		return err
	}
	if err := check("run.label", labelPlaceholders, append(slices.Clone(r.Run.Label), r.Run.Unlabel...)...); err != nil {
		return err
	}
	if err := check("run.assign", assigneePlaceholders, r.Run.Assign, r.Run.Unassign); err != nil {
		return err
	}
	for name, h := range r.Run.hooks() {
		if h == nil {
			continue
		}
		if err := check("run."+name+".label", labelPlaceholders, append(slices.Clone(h.Label), h.Unlabel...)...); err != nil {
			return err
		}
		assign := assigneePlaceholders
		if name == "on-start" {
			// The bot the task runs as is known when it starts.
			assign = map[string][]string{"*": append(slices.Clone(assigneePlaceholders["*"]), Bot), OnPR: assigneePlaceholders[OnPR]}
		}
		if err := check("run."+name+".assign", assign, h.Assign, h.Unassign); err != nil {
			return err
		}
	}
	inputs := make([]string, 0, len(r.Run.Inputs))
	for _, v := range r.Run.Inputs {
		inputs = append(inputs, v)
	}
	return check("run.inputs", inputPlaceholders, inputs...)
}

// Validate checks one rule on its own: its kind, its predicates for that
// kind, its action, its role and its policy's floors.
func (r *Rule) Validate() error {
	if r.Name == "" {
		return errors.New("a rule needs a name")
	}
	fail := func(format string, a ...any) error {
		return fmt.Errorf("rule %s: %s", r.Name, fmt.Sprintf(format, a...))
	}
	if !recipeNameRE.MatchString(r.Name) {
		// A rule's name names its file, and its task's (a rule task's).
		return fail("the name is not lowercase letters, digits and dashes, starting with a letter")
	}
	if _, ok := predicates[r.On]; !ok || r.On == "*" {
		return fail("on: %q is not one of %s, %s", r.On, OnIssue, OnPR)
	}
	if err := r.When.validate(r.On, false); err != nil {
		return fail("when%v", err)
	}
	if err := checkEnum("dedupe", r.Policy.Dedupe); err != nil {
		return fail("%v", err)
	}
	if err := checkEnum("revisit", r.Policy.Revisit); err != nil {
		return fail("%v", err)
	}
	if err := r.checkPlaceholders(); err != nil {
		return fail("%v", err)
	}

	if err := checkEnum("acknowledge", r.Run.Acknowledge); err != nil {
		return fail("run.%v", err)
	}
	switch {
	case r.Run.Recipe == "" && (r.Run.Acknowledge != "" ||
		r.Run.OnStart != nil || r.Run.OnSuccess != nil || r.Run.OnFailure != nil || r.Run.OnEnd != nil):
		return fail("run.acknowledge and the on-* hooks are a recipe's task's")
	case r.Run.OnStart != nil && r.Run.OnStart.Stop != nil, r.Run.OnSuccess != nil && r.Run.OnSuccess.Stop != nil, r.Run.OnEnd != nil && r.Run.OnEnd.Stop != nil:
		return fail("run: only on-failure may stop the item")
	case r.Run.Acknowledge != "" && r.On != OnPR:
		return fail("run.acknowledge is for a rule on pull requests")
	}
	switch {
	case r.Run.Recipe != "" && r.Run.writes():
		return fail("run is a recipe or GitHub writes, not both")
	case r.Run.Recipe == "" && !r.Run.writes():
		return fail("run needs a recipe or a GitHub write (label, unlabel, comment, assign, unassign, stop)")
	case r.Run.Recipe != "" && r.Source == SourceRepo:
		// A repository's rule may name the repository's own recipe
		// (.agents/recipes), which factory resolves when the task runs;
		// the watch checks it is there when it loads the rules.
		if !recipeNameRE.MatchString(r.Run.Recipe) {
			return fail("run.recipe %q is not a recipe name", r.Run.Recipe)
		}
	case r.Run.Recipe != "":
		if !slices.Contains(recipe.BuiltinNames(), r.Run.Recipe) {
			return fail("run.recipe %q is not one of factory's recipes", r.Run.Recipe)
		}
	}

	if c := r.Policy.Cooldown; c != nil {
		for _, d := range []Duration{c.Success, c.Failure} {
			if d.Duration != 0 && d.Duration < MinCooldown {
				return fail("policy.cooldown %s is under the floor of %s", d, MinCooldown)
			}
		}
		for _, e := range c.ResetOn {
			if err := checkEnum("reset-on", e); err != nil {
				return fail("policy.cooldown: %v", err)
			}
			if e == "head" && r.On != OnPR {
				return fail("policy.cooldown.reset-on: head is a PR's")
			}
		}
		if w := c.SkipWhen; w != nil {
			if got := w.set(); len(got) != 1 || got[0] != "assignee" {
				return fail("policy.cooldown.skip-when says assignee, and only that")
			}
			if err := checkEnum("assignee", w.Assignee); err != nil {
				return fail("policy.cooldown.skip-when: %v", err)
			}
		}
	}
	if g := r.Policy.GiveUp; g != nil {
		if g.Attempts < 1 || g.Attempts > MaxAttempts {
			return fail("policy.give-up.attempts %d: want 1 to %d", g.Attempts, MaxAttempts)
		}
		if err := checkSince(g.Since); err != nil {
			return fail("policy.give-up: %v", err)
		}
	}
	switch {
	case r.Order < 0:
		return fail("order %d: want above 0", r.Order)
	case r.IsExclusive() && r.Order == 0:
		return fail("a rule that takes the item needs an order")
	}
	return nil
}

// Set is a rule set: names unique, orders unique per kind of item.
type Set []*Rule

// Validate checks every rule and the set: unique names, and no two rules
// on one kind of item in the same place.
func (s Set) Validate() error {
	names := map[string]bool{}
	orders := map[string]map[int]string{}
	for _, r := range s {
		if err := r.Validate(); err != nil {
			return err
		}
		if names[r.Name] {
			return fmt.Errorf("two rules are named %s", r.Name)
		}
		names[r.Name] = true
		if r.Order == 0 {
			continue
		}
		if orders[r.On] == nil {
			orders[r.On] = map[int]string{}
		}
		if other := orders[r.On][r.Order]; other != "" {
			return fmt.Errorf("rules %s and %s on %s share order %d", other, r.Name, r.On, r.Order)
		}
		orders[r.On][r.Order] = r.Name
	}
	return nil
}

// Ordered is the set in order: by order, the rules without one after, by
// name.
func (s Set) Ordered() Set {
	out := slices.Clone(s)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if (a.Order == 0) != (b.Order == 0) {
			return b.Order == 0
		}
		if a.Order != b.Order {
			return a.Order < b.Order
		}
		return a.Name < b.Name
	})
	return out
}
