package commands

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/config"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/graph"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/watch/rules"
)

// NewGraphCommand is `factory graph`: issue graphs (design/issue-graph.md).
func NewGraphCommand(ctx context.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "graph",
		Short: "Issue graphs: steps under a parent issue, as sub-issues",
		Long: `Issue graphs: a set of steps under a parent issue, each a templated
GitHub issue, with after edges between them, at most maxActive open at a
time, and an optional finally step once every other step is done
(factory/design/issue-graph.md).

A graph's steps come from a template in the repository,
.agents/graphs/<name>.yaml, or from an issue's approved plan: the fenced
` + "`yaml graph`" + ` block of its plan comment.`,
	}
	cmd.AddCommand(newGraphValidateCommand(ctx))
	return cmd
}

func newGraphValidateCommand(ctx context.Context) *cobra.Command {
	var rawURL string
	cmd := &cobra.Command{
		Use:   "validate [<template.yaml>] [--url <issue>]",
		Short: "Check a graph template, or an issue's plan as a graph",
		Long: `Check a graph template file, or the steps of an issue's plan, as the
reconciler will before it creates anything.

A template (.agents/graphs/<name>.yaml): the schema (strict, apiVersion
` + graph.APIVersion + `, kind ` + graph.Kind + `), unique step ids, after
edges that name steps and form no cycle, then (fix, plan, ask) and
checkpoint (stop) values, constrained params with valid defaults and
examples, and every step rendered with each param's example and with every
optional param left out. A step's templates may reference only the steps
it comes after (` + "`{{ (step \"<id>\").Issue }}`" + `).

With --url, the issue's plan: the newest comment of key plan by a trusted
author kind (a pool bot, the owner, a member, a collaborator); its
` + "`yaml graph`" + ` block is checked the same way (no params; checkpoint
replan allowed). A plan without one is a one-PR plan.`,
		Example: `  factory graph validate .agents/graphs/kcc-greenfield.yaml
  factory graph validate --url https://github.com/owner/repo/issues/123`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			out := c.OutOrStdout()
			switch {
			case len(args) == 1 && rawURL != "":
				return fmt.Errorf("give a template file or --url, not both")
			case len(args) == 1:
				return validateGraphTemplate(out, args[0])
			case rawURL != "":
				return validateGraphPlan(ctx, out, rawURL)
			}
			return fmt.Errorf("give a template file or --url <issue>")
		},
	}
	cmd.Flags().StringVar(&rawURL, "url", "", "an issue whose plan comment's graph block to check")
	return cmd
}

func validateGraphTemplate(out io.Writer, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	t, err := graph.ParseTemplate(data)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if err := t.Validate(); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	fmt.Fprintf(out, "%s: graph %s is valid\n", path, t.Metadata.Name)
	describeGraph(out, &t.Spec)
	return nil
}

func validateGraphPlan(ctx context.Context, out io.Writer, rawURL string) error {
	item, err := parseGitHubItemURL(rawURL)
	if err != nil {
		return err
	}
	if item.IsPR {
		return fmt.Errorf("%s is a pull request; a plan is an issue's", rawURL)
	}
	gh, err := github.NewClient(ctx)
	if err != nil {
		return err
	}
	comments, err := github.ForRepo(gh, item.Owner, item.Repo).ListIssueComments(ctx, item.Number)
	if err != nil {
		return err
	}
	cfg, _ := config.LoadConfig()
	p, err := graph.PlanFromComments(comments, rules.Env{Pool: poolLogins(cfg), Members: cfg.TrustedLogins()})
	if err != nil {
		return err
	}
	switch {
	case p == nil:
		return fmt.Errorf("#%d has no plan comment by a trusted author (a pool bot, the owner, a member, a collaborator): plan it first", item.Number)
	case p.Steps == nil:
		fmt.Fprintf(out, "#%d: plan v%d has no graph block: a one-PR plan\n", item.Number, p.Version)
		return nil
	}
	if err := p.Steps.ValidatePlan(); err != nil {
		return fmt.Errorf("#%d plan v%d: %w", item.Number, p.Version, err)
	}
	fmt.Fprintf(out, "#%d: plan v%d (by @%s) is a valid graph\n", item.Number, p.Version, p.Comment.GetUser().GetLogin())
	describeGraph(out, p.Steps)
	return nil
}

// describeGraph prints the steps: what each comes after, what its child
// does first, its checkpoint.
func describeGraph(out io.Writer, s *graph.Spec) {
	fmt.Fprintf(out, "maxActive %d\n", s.ActiveLimit())
	line := func(id string, st graph.Step) {
		after := "-"
		if len(st.After) > 0 {
			after = strings.Join(st.After, ", ")
		}
		if id == graph.FinallyID {
			after = "every step"
		}
		extra := ""
		if len(st.Checkpoint) > 0 {
			extra = "  checkpoint " + strings.Join(st.Checkpoint, "+")
		}
		fmt.Fprintf(out, "  %-20s after %-24s then %-5s%s  %s\n", id, after, st.ThenOrDefault(), extra, st.Title)
	}
	for _, st := range s.Steps {
		line(st.ID, st)
	}
	if s.Finally != nil {
		line(graph.FinallyID, *s.Finally)
	}
}
