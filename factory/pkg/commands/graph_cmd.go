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
	cmd.AddCommand(newGraphReconcileCommand(ctx))
	cmd.AddCommand(newGraphAdoptCommand(ctx))
	cmd.AddCommand(newGraphStartCommand(ctx))
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

// graphFlags are what the commands that read or write a graph share.
type graphFlags struct {
	url      string
	trigger  string
	maxDepth int
	dryRun   bool
}

func (f *graphFlags) add(cmd *cobra.Command, dryRun string) {
	cmd.Flags().StringVar(&f.url, "url", "", "the parent issue's URL")
	cmd.Flags().StringVar(&f.trigger, "trigger-label", "", "the label a child to be fixed gets (default: the factory config's triggerLabel, as watch uses)")
	cmd.Flags().IntVar(&f.maxDepth, "max-depth", 0, "how deep graphs may nest (default 3)")
	cmd.Flags().BoolVar(&f.dryRun, "dry-run", false, dryRun)
	_ = cmd.MarkFlagRequired("url")
}

// client is the parent's repository client and the options a pass runs
// with: the caller's login, the factory config's pool and trusted logins.
func (f *graphFlags) client(ctx context.Context, out io.Writer) (*github.Client, graph.Options, error) {
	item, err := parseGitHubItemURL(f.url)
	if err != nil {
		return nil, graph.Options{}, err
	}
	if item.IsPR {
		return nil, graph.Options{}, fmt.Errorf("%s is a pull request; a graph's parent is an issue", f.url)
	}
	gh, err := github.NewClient(ctx)
	if err != nil {
		return nil, graph.Options{}, err
	}
	client := github.ForRepo(gh, item.Owner, item.Repo)
	login, err := client.AuthenticatedLogin(ctx)
	if err != nil {
		return nil, graph.Options{}, err
	}
	trigger := f.trigger
	if trigger == "" {
		trigger = watchTriggerLabel()
	}
	cfg, _ := config.LoadConfig()
	return client, graph.Options{
		Issue: item.Number, Trigger: trigger, BotLogin: login,
		Pool: poolLogins(cfg), Members: cfg.TrustedLogins(),
		MaxDepth: f.maxDepth, DryRun: f.dryRun,
		Logf: func(format string, args ...any) { fmt.Fprintf(out, format+"\n", args...) },
	}, nil
}

func newGraphReconcileCommand(ctx context.Context) *cobra.Command {
	var f graphFlags
	cmd := &cobra.Command{
		Use:   "reconcile --url <parent>",
		Short: "Run one pass over a graph's parent",
		Long: `Run one pass over a graph's parent issue, as the watch will.

A pass reads the parent's start block, its steps (a template's file on the
default branch, or the issue's plan), its children (its sub-issues, the
ones its status comment records, and the issues you recently created with
its marker) and their merged bot PRs, then decides (graph.Reconcile) and
writes, in order: repairs (duplicates closed, children linked), labels a
child to be fixed lost, children done by a merged bot PR closed,
checkpoints (the stop or plan label on the parent), the children of the
steps now ready (within maxActive), the parent closed once every step is
done - and the status comment.

With --dry-run it writes nothing, and prints what it would write and the
status comment.`,
		Example: `  factory graph reconcile --url https://github.com/owner/repo/issues/123 --dry-run`,
		Args:    cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			out := c.OutOrStdout()
			client, opts, err := f.client(ctx, out)
			if err != nil {
				return err
			}
			p, err := graph.Sync(ctx, client, opts)
			if err != nil {
				return err
			}
			if p.NoGraph {
				fmt.Fprintf(out, "#%d has no factory-graph start block: it is no graph's parent\n", opts.Issue)
				return nil
			}
			if f.dryRun {
				fmt.Fprintf(out, "\n%s\n", p.Status)
			} else {
				fmt.Fprintf(out, "#%d: %s\n", opts.Issue, p.Result.Phase)
			}
			return nil
		},
	}
	f.add(cmd, "write nothing; print what the pass would write")
	return cmd
}

func newGraphAdoptCommand(ctx context.Context) *cobra.Command {
	var f graphFlags
	var step string
	var issue int
	cmd := &cobra.Command{
		Use:   "adopt --url <parent> --step <id> --issue <n>",
		Short: "Bind an existing issue to a step of a graph",
		Long: `Bind an existing issue to a step of the parent's graph: its body gets the
step's marker, and it is linked as the parent's sub-issue. The next pass
counts it as the step's child.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			client, opts, err := f.client(ctx, c.OutOrStdout())
			if err != nil {
				return err
			}
			return graph.Adopt(ctx, client, opts, step, issue)
		},
	}
	f.add(cmd, "write nothing; print what it would do")
	cmd.Flags().StringVar(&step, "step", "", "the step's id")
	cmd.Flags().IntVar(&issue, "issue", 0, "the issue to bind to the step")
	_ = cmd.MarkFlagRequired("step")
	_ = cmd.MarkFlagRequired("issue")
	return cmd
}

func newGraphStartCommand(ctx context.Context) *cobra.Command {
	var f graphFlags
	var source, name string
	var params []string
	cmd := &cobra.Command{
		Use:   "start --url <issue> (--source plan | --graph <template> [--param k=v]...)",
		Short: "Make an issue a graph's parent",
		Long: `Make an issue a graph's parent: check the steps - the issue's plan
(--source plan) or a template and its params (--graph) - and post the
start block as a keyed comment, which a pass reads as it reads one in the
body. The bot cannot edit another account's issue body; this is how a rule
starts a graph. An issue that already has a start block is left as it is.`,
		Example: `  factory graph start --url https://github.com/owner/repo/issues/123 --source plan
  factory graph start --url https://github.com/owner/repo/issues/124 --graph kcc-greenfield --param kind=SpannerInstanceConfig`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			client, opts, err := f.client(ctx, c.OutOrStdout())
			if err != nil {
				return err
			}
			start := graph.Start{Graph: name, Source: source}
			for _, kv := range params {
				k, v, ok := strings.Cut(kv, "=")
				if !ok {
					return fmt.Errorf("--param %q: give name=value", kv)
				}
				if start.Params == nil {
					start.Params = map[string]string{}
				}
				start.Params[k] = v
			}
			return graph.StartGraph(ctx, client, opts, start)
		},
	}
	f.add(cmd, "write nothing; print what it would do")
	cmd.Flags().StringVar(&source, "source", "", "plan: the steps are the issue's plan's graph block")
	cmd.Flags().StringVar(&name, "graph", "", "a template: .agents/graphs/<name>.yaml")
	cmd.Flags().StringArrayVar(&params, "param", nil, "a template param, name=value (repeatable)")
	return cmd
}
