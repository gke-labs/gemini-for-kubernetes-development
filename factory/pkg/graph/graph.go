// Package graph is an issue graph's definition (design/issue-graph.md): a
// set of steps under a parent issue, each a templated GitHub issue, with
// after edges between them (a DAG), at most maxActive open at a time, and
// an optional finally step once every other step is done.
//
// A graph's steps come from a template in the repository
// (.agents/graphs/<name>.yaml, ParseTemplate) instantiated with params, or
// from an issue's approved plan: the fenced `yaml graph` block of its plan
// comment (ParsePlanBlock, PlanFromComments). Validate checks either; the
// reconciler that turns them into sub-issues is not here yet.
package graph

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// APIVersion and Kind are a template file's schema header: not a
// Kubernetes resource, a way for the format to evolve and for tools to
// recognise a graph file.
const (
	APIVersion = "factory.gemini.google.com/v1alpha1"
	Kind       = "IssueGraph"
)

// What a step's child does first (Step.Then).
const (
	ThenFix  = "fix"
	ThenPlan = "plan"
	ThenAsk  = "ask"
)

// What a checkpoint does when its step is done (Step.Checkpoint).
const (
	CheckpointStop   = "stop"
	CheckpointReplan = "replan"
)

// FinallyID is the finally step's id: no step may take it.
const FinallyID = "finally"

// Template is a graph template file, .agents/graphs/<name>.yaml.
type Template struct {
	APIVersion string   `yaml:"apiVersion"`
	Kind       string   `yaml:"kind"`
	Metadata   Metadata `yaml:"metadata"`
	Spec       Spec     `yaml:"spec"`
}

// Metadata names a template.
type Metadata struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description,omitempty"`
}

// Spec is a graph's steps and settings: a template's spec, or the whole of
// a plan's `yaml graph` block (which has no params).
type Spec struct {
	// Params are the inputs a template's start block supplies.
	Params []Param `yaml:"params,omitempty"`
	// MaxActive is how many steps may be open at once; 0 is the default, 1.
	MaxActive int      `yaml:"maxActive,omitempty"`
	Defaults  Defaults `yaml:"defaults,omitempty"`
	Steps     []Step   `yaml:"steps"`
	// Finally is the optional step created once every other step is done.
	Finally *Step `yaml:"finally,omitempty"`
}

// Defaults apply to every step unless the step overrides them.
type Defaults struct {
	Labels []string `yaml:"labels,omitempty"`
}

// Param is one input a template's start block supplies: constrained by a
// pattern or an enum, so free text never reaches a template.
type Param struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description,omitempty"`
	Pattern     string   `yaml:"pattern,omitempty"`
	Enum        []string `yaml:"enum,omitempty"`
	// Required is true unless set false.
	Required *bool  `yaml:"required,omitempty"`
	Default  string `yaml:"default,omitempty"`
	// Example is the value Validate renders the steps with.
	Example string `yaml:"example,omitempty"`
}

// IsRequired is whether the start block must supply the param.
func (p Param) IsRequired() bool { return p.Required == nil || *p.Required }

// Step is one node of the graph: a templated issue, and when it may start.
type Step struct {
	// ID identifies the step, and its child across passes; the finally
	// step has none (it is FinallyID).
	ID    string `yaml:"id,omitempty"`
	Title string `yaml:"title"`
	Body  string `yaml:"body"`
	// After are the steps that must be done before this one starts.
	After []string `yaml:"after,omitempty"`
	// Then is what the child does first: fix (default), plan or ask.
	Then string `yaml:"then,omitempty"`
	// Checkpoint is what the reconciler does when the step is done: stop
	// the graph, have the plan revised, or both.
	Checkpoint Checkpoint `yaml:"checkpoint,omitempty"`
	Labels     []string   `yaml:"labels,omitempty"`
	// Review is review instructions for the child's PR (a template).
	Review    string   `yaml:"review,omitempty"`
	Assignees []string `yaml:"assignees,omitempty"`
}

// ThenOrDefault is the step's then, fix when unset.
func (s Step) ThenOrDefault() string {
	if s.Then == "" {
		return ThenFix
	}
	return s.Then
}

// Checkpoint is one checkpoint action or a list of them.
type Checkpoint []string

// UnmarshalYAML reads one action or a list.
func (c *Checkpoint) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		var one string
		if err := n.Decode(&one); err != nil {
			return err
		}
		*c = nil
		if one != "" {
			*c = Checkpoint{one}
		}
		return nil
	}
	var many []string
	if err := n.Decode(&many); err != nil {
		return err
	}
	*c = many
	return nil
}

// Has is whether the checkpoint includes action.
func (c Checkpoint) Has(action string) bool {
	for _, a := range c {
		if a == action {
			return true
		}
	}
	return false
}

// ActiveLimit is how many steps may be open at once.
func (s Spec) ActiveLimit() int {
	if s.MaxActive <= 0 {
		return 1
	}
	return s.MaxActive
}

// Step is the step of id, the finally step for FinallyID; nil for none.
func (s Spec) Step(id string) *Step {
	if id == FinallyID {
		return s.Finally
	}
	for i := range s.Steps {
		if s.Steps[i].ID == id {
			return &s.Steps[i]
		}
	}
	return nil
}

// ParseTemplate reads a template file strictly: an unknown field is an
// error, and the schema header must be this one.
func ParseTemplate(data []byte) (*Template, error) {
	var t Template
	if err := decodeStrict(data, &t); err != nil {
		return nil, err
	}
	if t.APIVersion != APIVersion || t.Kind != Kind {
		return nil, fmt.Errorf("apiVersion/kind is %q/%q, not %s/%s", t.APIVersion, t.Kind, APIVersion, Kind)
	}
	if t.Metadata.Name == "" {
		return nil, errors.New("metadata.name is not set")
	}
	return &t, nil
}

// ParsePlanBlock reads a plan's steps: the first fenced `yaml graph` block
// in markdown, strictly. A plan with no such block has no steps: nil, no
// error.
func ParsePlanBlock(markdown string) (*Spec, error) {
	block, ok := planBlock(markdown)
	if !ok {
		return nil, nil
	}
	var s Spec
	if err := decodeStrict([]byte(block), &s); err != nil {
		return nil, fmt.Errorf("the plan's graph block: %w", err)
	}
	if len(s.Params) > 0 {
		return nil, errors.New("the plan's graph block: params are a template's; a plan's steps have none")
	}
	return &s, nil
}

// planFenceRE opens a `yaml graph` fenced block: three or more backticks,
// then the info string.
var planFenceRE = regexp.MustCompile("^(`{3,})\\s*yaml\\s+graph\\s*$")

// planBlock is the content of markdown's first `yaml graph` block.
func planBlock(markdown string) (string, bool) {
	lines := strings.Split(markdown, "\n")
	for i, line := range lines {
		m := planFenceRE.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if m == nil {
			continue
		}
		fence := m[1]
		var body []string
		for _, l := range lines[i+1:] {
			if strings.TrimSpace(strings.TrimRight(l, "\r")) == fence {
				return strings.Join(body, "\n"), true
			}
			body = append(body, l)
		}
		return "", false // not closed
	}
	return "", false
}

func decodeStrict(data []byte, out any) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil {
		if errors.Is(err, io.EOF) {
			return errors.New("empty")
		}
		return err
	}
	return nil
}
