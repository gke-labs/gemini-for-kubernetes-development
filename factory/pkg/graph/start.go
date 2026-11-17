package graph

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	githubv39 "github.com/google/go-github/v39/github"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/watch/rules"
	"gopkg.in/yaml.v3"
)

// StartKey is the comment key a start block is posted under, when a rule
// starts a graph: the bot cannot edit another account's issue body.
const StartKey = "graph"

// SourcePlan is a start block's source for steps from the issue's plan.
const SourcePlan = "plan"

// Start is a graph's start block: where its steps come from - a template
// (Graph) or the issue's plan (Source: plan) - and a template's params.
type Start struct {
	Graph  string            `yaml:"graph,omitempty"`
	Source string            `yaml:"source,omitempty"`
	Params map[string]string `yaml:"params,omitempty"`
	// Grouped runs consecutive steps in one PR (Spec.GroupPrev), each
	// starting once the one before has pushed to it and its checks are
	// green, until a step with group-gate. False, the default, is one PR
	// per step, each merged before the next starts.
	Grouped bool `yaml:"grouped,omitempty"`
	// Gates, when given, are this graph's group gates (grouped only): the
	// steps whose group's PR ends with them and merges before any step
	// after them starts, in place of the steps' own group-gate. An empty
	// list gates none: the steps in a line are one PR. Not given (nil),
	// the steps' group-gate decides.
	Gates []string `yaml:"gates,omitempty"`
	// Done are steps whose work is already on the default branch, done
	// outside this graph, with a receipt each: the merged PR that did it
	// ("#123"), or ReceiptMaster when no one PR did. Such a step gets no
	// child: it counts as succeeded, and the steps after it start. A step
	// that has a child already keeps it (the child wins).
	Done Done `yaml:"done,omitempty"`
}

// ReceiptMaster is a done step's receipt when its work is on the default
// branch but no one PR did it.
const ReceiptMaster = "master"

// Done is a start block's done steps, step id to receipt. The block may
// write it as a list of step ids (each ReceiptMaster) or as a map.
type Done map[string]string

var receiptRE = regexp.MustCompile(`^#[1-9][0-9]*$`)

// UnmarshalYAML reads a list of step ids or a map of step id to receipt.
func (d *Done) UnmarshalYAML(n *yaml.Node) error {
	out := Done{}
	switch n.Kind {
	case yaml.SequenceNode:
		var ids []string
		if err := n.Decode(&ids); err != nil {
			return err
		}
		for _, id := range ids {
			out[id] = ReceiptMaster
		}
	case yaml.MappingNode:
		var m map[string]string
		if err := n.Decode(&m); err != nil {
			return err
		}
		for id, r := range m {
			out[id] = strings.TrimSpace(r)
		}
	default:
		return fmt.Errorf("done: a list of steps, or a map of step to receipt (#<PR> or %s)", ReceiptMaster)
	}
	*d = out
	return nil
}

// PR is a receipt's PR number, 0 for ReceiptMaster.
func (d Done) PR(step string) int {
	r := d[step]
	if !receiptRE.MatchString(r) {
		return 0
	}
	n, _ := strconv.Atoi(r[1:])
	return n
}

// SourceName is what a child's marker records: the template's name, or
// "plan".
func (s Start) SourceName() string {
	if s.Graph != "" {
		return s.Graph
	}
	return s.Source
}

var (
	startFenceRE  = regexp.MustCompile("^(`{3,})\\s*factory-graph\\s*$")
	graphNameRE   = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	startMarkerRE = regexp.MustCompile(`<!-- factory:comment key=` + StartKey + ` v=\d+ -->`)
)

// ParseStart reads the first fenced `factory-graph` block in markdown: nil,
// no error, when there is none.
func ParseStart(markdown string) (*Start, error) {
	lines := strings.Split(markdown, "\n")
	for i, line := range lines {
		m := startFenceRE.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if m == nil {
			continue
		}
		var body []string
		for _, l := range lines[i+1:] {
			if strings.TrimSpace(strings.TrimRight(l, "\r")) == m[1] {
				var s Start
				if err := decodeStrict([]byte(strings.Join(body, "\n")), &s); err != nil {
					return nil, fmt.Errorf("the factory-graph block: %w", err)
				}
				return &s, s.validate()
			}
			body = append(body, l)
		}
		return nil, fmt.Errorf("the factory-graph block is not closed")
	}
	return nil, nil
}

func (s *Start) validate() error {
	switch {
	case s.Graph != "" && s.Source != "":
		return fmt.Errorf("the factory-graph block names both a graph and a source; give one")
	case s.Graph == "" && s.Source == "":
		return fmt.Errorf("the factory-graph block names neither a graph (a template) nor source: plan")
	case s.Source != "" && s.Source != SourcePlan:
		return fmt.Errorf("source %q is not %q", s.Source, SourcePlan)
	case s.Graph != "" && !graphNameRE.MatchString(s.Graph):
		return fmt.Errorf("graph %q: a bare name, resolved to .agents/graphs/<name>.yaml in this repository", s.Graph)
	case s.Source == SourcePlan && len(s.Params) > 0:
		return fmt.Errorf("params are a template's; a plan's steps take none")
	case s.Gates != nil && !s.Grouped:
		return fmt.Errorf("gates are a grouped graph's (grouped: true); without it every step is its own PR")
	}
	for id, r := range s.Done {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("done: a step with no name")
		}
		if r != ReceiptMaster && !receiptRE.MatchString(r) {
			return fmt.Errorf("done: %s: the receipt %q is neither a merged PR (#123) nor %s", id, r, ReceiptMaster)
		}
	}
	return nil
}

// WithGates is spec with the start block's gates as its steps' group gates,
// when the block gives them: each named step a gate, every other step
// not. A gate that names no step is an error; the finally step opens its
// own PR anyway, so naming it gates nothing more. The spec itself is not
// changed.
func (s Start) WithGates(spec *Spec) (*Spec, error) {
	if s.Gates == nil || spec == nil {
		return spec, nil
	}
	for _, g := range s.Gates {
		if g == FinallyID {
			continue
		}
		if spec.Step(g) == nil {
			return nil, fmt.Errorf("gates: %s is not a step of the graph", g)
		}
	}
	out := *spec
	out.Steps = slices.Clone(spec.Steps)
	for i := range out.Steps {
		out.Steps[i].GroupGate = slices.Contains(s.Gates, out.Steps[i].ID)
	}
	return &out, nil
}

// checkDone is whether the block's done steps are all steps of spec.
func (s Start) checkDone(spec *Spec) error {
	if spec == nil {
		return nil
	}
	for _, id := range slices.Sorted(maps.Keys(s.Done)) {
		if id != FinallyID && spec.Step(id) == nil {
			return fmt.Errorf("done: %s is not a step of the graph", id)
		}
	}
	return nil
}

// StartBlock renders a start block, as the start command posts it.
func (s Start) StartBlock() string {
	var b strings.Builder
	b.WriteString("```factory-graph\n")
	if s.Graph != "" {
		fmt.Fprintf(&b, "graph: %s\n", s.Graph)
	} else {
		fmt.Fprintf(&b, "source: %s\n", s.Source)
	}
	if s.Grouped {
		b.WriteString("grouped: true\n")
	}
	if s.Gates != nil {
		fmt.Fprintf(&b, "gates: [%s]\n", strings.Join(s.Gates, ", "))
	}
	if len(s.Done) > 0 {
		b.WriteString("done:\n")
		for _, id := range slices.Sorted(maps.Keys(s.Done)) {
			fmt.Fprintf(&b, "  %s: %q\n", id, s.Done[id])
		}
	}
	if len(s.Params) > 0 {
		b.WriteString("params:\n")
		keys := make([]string, 0, len(s.Params))
		for k := range s.Params {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "  %s: %q\n", k, s.Params[k])
		}
	}
	b.WriteString("```")
	return b.String()
}

// FindStart is an issue's start block: in its body, else in a keyed
// comment (key graph) by a trusted author kind (TrustedKinds). Nil, no
// error, when it has none.
func FindStart(body string, comments []*githubv39.IssueComment, env rules.Env) (*Start, error) {
	if s, err := ParseStart(body); s != nil || err != nil {
		return s, err
	}
	for i := len(comments) - 1; i >= 0; i-- {
		c := comments[i]
		if !startMarkerRE.MatchString(c.GetBody()) {
			continue
		}
		kind := rules.AuthorKind(rules.Item{Author: c.GetUser().GetLogin(), AuthorAssociation: c.GetAuthorAssociation()}, env)
		if !slices.Contains(TrustedKinds, kind) {
			continue
		}
		return ParseStart(c.GetBody())
	}
	return nil, nil
}

// StartComment is the keyed comment a rule starts a graph with.
func (s Start) StartComment() string {
	return "Starting an issue graph from this issue's " + s.describe() + ".\n\n" + s.StartBlock() + "\n\n<!-- factory:comment key=" + StartKey + " v=1 -->"
}

func (s Start) describe() string {
	if s.Graph != "" {
		return "template `" + s.Graph + "`"
	}
	return "plan"
}
