package graph

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	githubv39 "github.com/google/go-github/v39/github"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/watch/rules"
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
