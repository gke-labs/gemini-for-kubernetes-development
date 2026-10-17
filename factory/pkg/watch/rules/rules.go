// Package rules is the model of the watch loop's rules
// (factory/design/watch-rules.md): a GitHub condition and the action taken
// on it, one rule per file. The prebaked rules, factory's own, are embedded
// here, one file each, and describe what today's watch does. Nothing here
// evaluates a rule or acts on one yet: the engine that does is
// factory/design/watch-rules.md's Phase 2a-2.
//
// Fan-out, the warm workspace and the sandbox GC are not rules: they stay
// plugins of the watch, as in the design.
package rules

import (
	"bytes"
	"errors"
	"fmt"
	"io"
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
	OnIssue = "issue"
	OnPR    = "pr"
	// OnSchedule is a scheduled chore's: an agent definition under
	// .agents/ whose schedule is due.
	OnSchedule = "schedule"
	// OnEvent is a rule that fires on an event the engine derives rather
	// than lists: today, an issue's closure confirmed by the sandbox GC.
	OnEvent = "event"
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
	// On is what the rule looks at: OnIssue, OnPR, OnSchedule or OnEvent.
	On string `yaml:"on" json:"on"`
	// Enabled turns the rule off when false; unset is on. A rule's own
	// file sets it, and a repository's file of the same name with only a
	// name and enabled turns factory's rule on or off (IsToggle, Overlay).
	Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	// Group makes the rule one of an ordered, exclusive set on an item.
	Group *Group `yaml:"group,omitempty" json:"group,omitempty"`
	// When is the condition: every predicate set must hold.
	When When `yaml:"when" json:"when"`
	// Run is the action.
	Run Run `yaml:"run" json:"run"`
	// As is the role the action runs as: coder, agent, reviewer, author
	// (the PR's author, from the pool) or watcher (the watcher's own
	// token, for GitHub writes).
	As string `yaml:"as,omitempty" json:"as,omitempty"`
	// Policy is the rule's dedupe, cooldown, give-up and ordering.
	Policy Policy `yaml:"policy,omitempty" json:"policy,omitzero"`
	// Expect are the facts a queued task is checked against before it
	// starts (preconditions): head, open, no-linked-pr.
	Expect []string `yaml:"expect,omitempty" json:"expect,omitempty"`

	// Source is whose rule it is, SourceFactory or SourceRepo, and
	// EnabledBy whose its Enabled is when a repository's toggle set it;
	// neither is written in a rule file.
	Source    string `yaml:"-" json:"source,omitempty"`
	EnabledBy string `yaml:"-" json:"enabledBy,omitempty"`
}

// IsEnabled reports whether the rule is on.
func (r *Rule) IsEnabled() bool { return r.Enabled == nil || *r.Enabled }

// Group places a rule in an ordered, exclusive set of rules on one item:
// the first of the group whose condition holds is taken, none while a task
// of the item runs, and the rule ordered last holds when no other does.
type Group struct {
	Name  string `yaml:"name" json:"name"`
	Order int    `yaml:"order,omitempty" json:"order,omitempty"`
	Last  bool   `yaml:"last,omitempty" json:"last,omitempty"`
}

// Labels matches an item's labels: any of Any, all of All, none of None.
// A label may name the trigger ({{trigger}}) or a glob of a prefix
// ("*/review").
type Labels struct {
	Any  []string `yaml:"any,omitempty" json:"any,omitempty"`
	All  []string `yaml:"all,omitempty" json:"all,omitempty"`
	None []string `yaml:"none,omitempty" json:"none,omitempty"`
}

// Attempts holds while the rule's runs on the item since Since (head: the
// PR's head changed; human: a trusted human spoke) are fewer than Max.
type Attempts struct {
	Rule  string `yaml:"rule,omitempty" json:"rule,omitempty"`
	Since string `yaml:"since,omitempty" json:"since,omitempty"`
	Max   int    `yaml:"max" json:"max"`
}

// When is the closed set of predicates (design: Predicates). A nil or empty
// field is not checked.
type When struct {
	// Any item.
	Labels          *Labels   `yaml:"labels,omitempty" json:"labels,omitempty"`
	Assignee        string    `yaml:"assignee,omitempty" json:"assignee,omitempty"` // pool | none | owner
	Author          string    `yaml:"author,omitempty" json:"author,omitempty"`     // pool | trusted
	Stopped         *bool     `yaml:"stopped,omitempty" json:"stopped,omitempty"`
	UpdatedSinceRun *bool     `yaml:"updated-since-run,omitempty" json:"updated-since-run,omitempty"`
	Attempts        *Attempts `yaml:"attempts,omitempty" json:"attempts,omitempty"`

	// Issues.
	LinkedPR string `yaml:"linked-pr,omitempty" json:"linked-pr,omitempty"` // none | open
	Workflow string `yaml:"workflow,omitempty" json:"workflow,omitempty"`   // none | any
	OpenedBy string `yaml:"opened-by,omitempty" json:"opened-by,omitempty"` // watcher
	Fanout   string `yaml:"fanout,omitempty" json:"fanout,omitempty"`       // none | parent | child

	// Pull requests.
	Checks         string   `yaml:"checks,omitempty" json:"checks,omitempty"` // failing | pending | green
	Mergeable      *bool    `yaml:"mergeable,omitempty" json:"mergeable,omitempty"`
	Feedback       string   `yaml:"feedback,omitempty" json:"feedback,omitempty"` // new-trusted
	ReviewedAtHead *bool    `yaml:"reviewed-at-head,omitempty" json:"reviewed-at-head,omitempty"`
	Approved       *bool    `yaml:"approved,omitempty" json:"approved,omitempty"`
	Draft          *bool    `yaml:"draft,omitempty" json:"draft,omitempty"`
	MergeQueue     *bool    `yaml:"merge-queue,omitempty" json:"merge-queue,omitempty"`
	InactiveFor    Duration `yaml:"inactive-for,omitempty" json:"inactive-for,omitzero"`

	// Schedules: the agent definition's schedule is due.
	Due *bool `yaml:"due,omitempty" json:"due,omitempty"`

	// Events: which one.
	Event string `yaml:"event,omitempty" json:"event,omitempty"` // linked-issue-closed
}

// Run is the action: a recipe run, or GitHub writes.
type Run struct {
	// Recipe is a factory recipe to run on the item, Apply the action its
	// result is applied with when the run ends, Inputs its inputs.
	Recipe string            `yaml:"recipe,omitempty" json:"recipe,omitempty"`
	Apply  string            `yaml:"apply,omitempty" json:"apply,omitempty"`
	Inputs map[string]string `yaml:"inputs,omitempty" json:"inputs,omitempty"`

	// GitHub writes, done by the watcher.
	Label    []string `yaml:"label,omitempty" json:"label,omitempty"`
	Unlabel  []string `yaml:"unlabel,omitempty" json:"unlabel,omitempty"`
	Comment  string   `yaml:"comment,omitempty" json:"comment,omitempty"`
	Assign   string   `yaml:"assign,omitempty" json:"assign,omitempty"`
	Unassign string   `yaml:"unassign,omitempty" json:"unassign,omitempty"`
	// Stop adds the stop label, with a comment saying why.
	Stop *Stop `yaml:"stop,omitempty" json:"stop,omitempty"`
}

// Stop is the give-up action: the stop label and a comment.
type Stop struct {
	Comment string `yaml:"comment,omitempty" json:"comment,omitempty"`
}

func (r Run) writes() bool {
	return len(r.Label) > 0 || len(r.Unlabel) > 0 || r.Comment != "" || r.Assign != "" || r.Unassign != "" || r.Stop != nil
}

// Policy is a rule's dedupe, cooldown, give-up and ordering.
type Policy struct {
	// Dedupe is item (one task per item and rule) or head (once per PR
	// head).
	Dedupe   string    `yaml:"dedupe,omitempty" json:"dedupe,omitempty"`
	Cooldown *Cooldown `yaml:"cooldown,omitempty" json:"cooldown,omitempty"`
	GiveUp   *GiveUp   `yaml:"give-up,omitempty" json:"give-up,omitempty"`
	// Phase orders the rule's tasks after the priority label: lower first.
	Phase int `yaml:"phase,omitempty" json:"phase,omitempty"`
	// IdentityPin pins the run to the bot that owns the item's sandbox;
	// unset is on.
	IdentityPin *bool `yaml:"identity-pin,omitempty" json:"identity-pin,omitempty"`
}

// Cooldown is how long after a run, by its outcome, the rule waits before
// it runs on the item again.
type Cooldown struct {
	Success Duration `yaml:"success,omitempty" json:"success,omitzero"`
	Failure Duration `yaml:"failure,omitempty" json:"failure,omitzero"`
}

// GiveUp stops the rule on an item after Attempts runs since Since, with
// Then (stop: the stop label and a comment).
type GiveUp struct {
	Attempts int    `yaml:"attempts" json:"attempts"`
	Since    string `yaml:"since,omitempty" json:"since,omitempty"`
	Then     string `yaml:"then,omitempty" json:"then,omitempty"`
	Comment  string `yaml:"comment,omitempty" json:"comment,omitempty"`
}

// Duration is a time.Duration written as Go writes one ("10m", "2h").
type Duration struct{ time.Duration }

// UnmarshalYAML reads a duration string.
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	if s == "" {
		d.Duration = 0
		return nil
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("line %d: %q is not a duration (e.g. 10m, 2h): %w", n.Line, s, err)
	}
	d.Duration = v
	return nil
}

// String writes 10m and 2h, not 10m0s and 2h0m0s.
func (d Duration) String() string {
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
	if d.Duration == 0 {
		return nil, nil
	}
	return d.String(), nil
}

// IsZero lets omitempty leave a zero duration out.
func (d Duration) IsZero() bool { return d.Duration == 0 }

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
	"*":        {"labels", "assignee", "author", "stopped", "updated-since-run", "attempts"},
	OnIssue:    {"linked-pr", "workflow", "opened-by", "fanout"},
	OnPR:       {"checks", "mergeable", "feedback", "reviewed-at-head", "approved", "draft", "merge-queue", "inactive-for"},
	OnSchedule: {"due"},
	OnEvent:    {"event"},
}

// set lists the When fields that are set, by YAML name.
func (w When) set() []string {
	var out []string
	add := func(name string, ok bool) {
		if ok {
			out = append(out, name)
		}
	}
	add("labels", w.Labels != nil)
	add("assignee", w.Assignee != "")
	add("author", w.Author != "")
	add("stopped", w.Stopped != nil)
	add("updated-since-run", w.UpdatedSinceRun != nil)
	add("attempts", w.Attempts != nil)
	add("linked-pr", w.LinkedPR != "")
	add("workflow", w.Workflow != "")
	add("opened-by", w.OpenedBy != "")
	add("fanout", w.Fanout != "")
	add("checks", w.Checks != "")
	add("mergeable", w.Mergeable != nil)
	add("feedback", w.Feedback != "")
	add("reviewed-at-head", w.ReviewedAtHead != nil)
	add("approved", w.Approved != nil)
	add("draft", w.Draft != nil)
	add("merge-queue", w.MergeQueue != nil)
	add("inactive-for", w.InactiveFor.Duration != 0)
	add("due", w.Due != nil)
	add("event", w.Event != "")
	return out
}

// enums are the values a string predicate or field may take.
var enums = map[string][]string{
	"assignee":  {"pool", "none", "owner"},
	"author":    {"pool", "trusted"},
	"linked-pr": {"none", "open"},
	"workflow":  {"none", "any"},
	"opened-by": {"watcher"},
	"fanout":    {"none", "parent", "child"},
	"checks":    {"failing", "pending", "green"},
	"feedback":  {"new-trusted"},
	"event":     {"linked-issue-closed"},
	"as":        {"coder", "agent", "reviewer", "author", "watcher"},
	"dedupe":    {"item", "head"},
	"since":     {"head", "human"},
	"then":      {"stop"},
}

func checkEnum(field, value string) error {
	if value == "" || slices.Contains(enums[field], value) {
		return nil
	}
	return fmt.Errorf("%s: %q is not one of %s", field, value, strings.Join(enums[field], ", "))
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
	if _, ok := predicates[r.On]; !ok || r.On == "*" {
		return fail("on: %q is not one of %s, %s, %s, %s", r.On, OnIssue, OnPR, OnSchedule, OnEvent)
	}
	allowed := append(slices.Clone(predicates["*"]), predicates[r.On]...)
	set := r.When.set()
	for _, p := range set {
		if !slices.Contains(allowed, p) {
			return fail("when.%s is not a predicate of a rule on %s", p, r.On)
		}
	}
	for field, value := range map[string]string{
		"assignee": r.When.Assignee, "author": r.When.Author, "linked-pr": r.When.LinkedPR,
		"workflow": r.When.Workflow, "opened-by": r.When.OpenedBy, "fanout": r.When.Fanout,
		"checks": r.When.Checks, "feedback": r.When.Feedback, "event": r.When.Event,
		"as": r.As, "dedupe": r.Policy.Dedupe,
	} {
		if err := checkEnum(field, value); err != nil {
			return fail("%v", err)
		}
	}
	// A rule on issues or PRs is listed from GitHub: it needs a predicate a
	// listing narrows on, or it would list every open item (design: the
	// GitHub cost and the repository's rules risks).
	if r.On == OnIssue || r.On == OnPR {
		narrows := r.When.Assignee == "pool" || r.When.Assignee == "owner" || r.When.Author != "" || r.When.OpenedBy != "" ||
			(r.When.Labels != nil && (len(r.When.Labels.Any) > 0 || len(r.When.Labels.All) > 0))
		if !narrows {
			return fail("when needs a predicate a listing can narrow on: labels (any or all), assignee (pool or owner), author or opened-by")
		}
	}
	if r.On == OnEvent && r.When.Event == "" {
		return fail("a rule on events needs when.event")
	}
	if a := r.When.Attempts; a != nil {
		if a.Max < 1 || a.Max > MaxAttempts {
			return fail("when.attempts.max %d: want 1 to %d", a.Max, MaxAttempts)
		}
		if err := checkEnum("since", a.Since); err != nil {
			return fail("when.attempts: %v", err)
		}
	}

	switch {
	case r.Run.Recipe != "" && r.Run.writes():
		return fail("run is a recipe or GitHub writes, not both")
	case r.Run.Recipe == "" && !r.Run.writes():
		return fail("run needs a recipe or a GitHub write (label, unlabel, comment, assign, unassign, stop)")
	case r.Run.Recipe != "":
		if !slices.Contains(recipe.BuiltinNames(), r.Run.Recipe) {
			return fail("run.recipe %q is not one of factory's recipes", r.Run.Recipe)
		}
		if r.As == "" || r.As == "watcher" {
			return fail("a recipe runs as coder, agent, reviewer or author")
		}
	case r.As != "" && r.As != "watcher":
		return fail("GitHub writes are the watcher's: as is watcher or unset")
	}

	if c := r.Policy.Cooldown; c != nil {
		for _, d := range []Duration{c.Success, c.Failure} {
			if d.Duration != 0 && d.Duration < MinCooldown {
				return fail("policy.cooldown %s is under the floor of %s", d, MinCooldown)
			}
		}
	}
	if g := r.Policy.GiveUp; g != nil {
		if g.Attempts < 1 || g.Attempts > MaxAttempts {
			return fail("policy.give-up.attempts %d: want 1 to %d", g.Attempts, MaxAttempts)
		}
		if err := checkEnum("since", g.Since); err != nil {
			return fail("policy.give-up: %v", err)
		}
		if err := checkEnum("then", g.Then); err != nil {
			return fail("policy.give-up: %v", err)
		}
	}
	for _, e := range r.Expect {
		switch {
		case e == "open":
		case e == "head" && r.On == OnPR:
		case e == "no-linked-pr" && r.On == OnIssue:
		default:
			return fail("expect %q: want open, head (on pr) or no-linked-pr (on issue)", e)
		}
	}
	if g := r.Group; g != nil {
		if g.Name == "" {
			return fail("group needs a name")
		}
		if (g.Order > 0) == g.Last {
			return fail("group %s: give an order above 0 or last, one of them", g.Name)
		}
	}
	return nil
}

// Set is a rule set: names unique, groups consistent.
type Set []*Rule

// Validate checks every rule and the set: unique names; in a group, one
// kind of item, unique orders and at most one last rule.
func (s Set) Validate() error {
	names := map[string]bool{}
	type member struct {
		on    string
		order map[int]string
		last  string
	}
	groups := map[string]*member{}
	for _, r := range s {
		if err := r.Validate(); err != nil {
			return err
		}
		if names[r.Name] {
			return fmt.Errorf("two rules are named %s", r.Name)
		}
		names[r.Name] = true
		if r.Group == nil {
			continue
		}
		g := groups[r.Group.Name]
		if g == nil {
			g = &member{on: r.On, order: map[int]string{}}
			groups[r.Group.Name] = g
		}
		if g.on != r.On {
			return fmt.Errorf("group %s holds rules on %s and on %s", r.Group.Name, g.on, r.On)
		}
		switch {
		case r.Group.Last && g.last != "":
			return fmt.Errorf("group %s: %s and %s are both last", r.Group.Name, g.last, r.Name)
		case r.Group.Last:
			g.last = r.Name
		case g.order[r.Group.Order] != "":
			return fmt.Errorf("group %s: %s and %s share order %d", r.Group.Name, g.order[r.Group.Order], r.Name, r.Group.Order)
		default:
			g.order[r.Group.Order] = r.Name
		}
	}
	return nil
}

// Groups are the set's groups, each its rules in order, the last one last,
// by group name.
func (s Set) Groups() map[string][]*Rule {
	out := map[string][]*Rule{}
	for _, r := range s {
		if r.Group != nil {
			out[r.Group.Name] = append(out[r.Group.Name], r)
		}
	}
	for _, rs := range out {
		sort.SliceStable(rs, func(i, j int) bool {
			a, b := rs[i].Group, rs[j].Group
			if a.Last != b.Last {
				return b.Last
			}
			return a.Order < b.Order
		})
	}
	return out
}
