package graph

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"

	githubv39 "github.com/google/go-github/v39/github"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/watch/rules"
)

// PlanKey is the comment key of an issue's plan comment, the one the plan
// recipe keeps (task-output.comment.key) and revises in place.
const PlanKey = "plan"

// TrustedKinds are the author kinds whose plan comment is an issue's plan:
// the pool's bots, who write it, and maintainers, who may write or edit it
// by hand. Anyone else's comment carrying the marker is not the plan.
var TrustedKinds = []string{rules.AuthorPool, rules.AuthorOwner, rules.AuthorMember, rules.AuthorCollaborator}

var planMarkerRE = regexp.MustCompile(`<!-- factory:comment key=` + PlanKey + ` v=(\d+) -->`)

// Plan is an issue's plan as a graph source: the comment, its version, and
// the steps of its `yaml graph` block (nil when it has none: a one-PR plan).
type Plan struct {
	Comment *githubv39.IssueComment
	Version int
	Steps   *Spec
}

// PlanFromComments finds an issue's plan among its comments: the newest
// comment of key plan whose author is of a trusted kind (TrustedKinds, as
// env classifies authors: env.Pool the pool's bots, env.Members the logins
// the overseer trusts as members). No such comment is nil, no error; a
// plan whose graph block does not parse is an error.
func PlanFromComments(comments []*githubv39.IssueComment, env rules.Env) (*Plan, error) {
	found, version := trustedPlanComment(comments, env)
	if found == nil {
		return nil, nil
	}
	steps, err := ParsePlanBlock(found.GetBody())
	if err != nil {
		return nil, fmt.Errorf("plan v%d (comment %d): %w", version, found.GetID(), err)
	}
	return &Plan{Comment: found, Version: version, Steps: steps}, nil
}

// trustedPlanComment is the newest comment of key plan by a trusted author kind,
// and its version; nil when there is none.
func trustedPlanComment(comments []*githubv39.IssueComment, env rules.Env) (*githubv39.IssueComment, int) {
	var found *githubv39.IssueComment
	version := 0
	for _, c := range comments {
		m := planMarkerRE.FindAllStringSubmatch(c.GetBody(), -1)
		if m == nil {
			continue
		}
		kind := rules.AuthorKind(rules.Item{Author: c.GetUser().GetLogin(), AuthorAssociation: c.GetAuthorAssociation()}, env)
		if !slices.Contains(TrustedKinds, kind) {
			continue
		}
		if found == nil || !c.GetCreatedAt().Before(found.GetCreatedAt()) {
			found = c
			version, _ = strconv.Atoi(m[len(m)-1][1])
		}
	}
	return found, version
}

// The shapes of an issue's plan (the rules' plan fact, PlanShape).
const (
	ShapePR    = "pr"
	ShapeGraph = "graph"
)

// PlanShape is the shape of an issue's plan, the rules' plan fact: graph
// when its plan comment (PlanFromComments' choice) has a `yaml graph`
// block, valid or not - a graph's start reports what is wrong with it -
// pr when it has none, "" when the issue has no plan comment.
func PlanShape(comments []*githubv39.IssueComment, env rules.Env) string {
	found, _ := trustedPlanComment(comments, env)
	switch {
	case found == nil:
		return ""
	case HasPlanBlock(found.GetBody()):
		return ShapeGraph
	}
	return ShapePR
}

// HasPlanBlock is whether markdown has a `yaml graph` block.
func HasPlanBlock(markdown string) bool {
	_, ok := planBlock(markdown)
	return ok
}
