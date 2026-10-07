package commands

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	githubv39 "github.com/google/go-github/v39/github"
	"github.com/spf13/cobra"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/envd"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
	factorysandbox "github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/sandbox"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/taskoutput"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/tasks"
)

// NewApplyCommand acts on task outputs: what a task found, applied to
// GitHub by whoever runs it, with their token. Tasks never write their
// results themselves, so a result can be looked at, edited, or dropped
// before anything is written.
func NewApplyCommand(ctx context.Context) *cobra.Command {
	var file, action string
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "apply -f <file | -> [--action <verb>]",
		Short: "Apply task outputs (a triage, a plan, notes, a review, a change …) to GitHub",
		Long: `Apply task outputs to GitHub.

A task output is the result a task leaves in its task directory
(task-output.yaml): its kind, the issue, PR or repository it is about,
the result, and the actions it offers. ` + "`factory sandbox task output`" + ` prints
it. Applying it writes it to GitHub with your credentials (GITHUB_TOKEN,
else gh's).

Without --action, apply does each write the result offers:
  Triage  label: adds the labels; comment: comments the assessment
  Plan    comment: comments the plan
  Notes   push-notes: commits the notes to docs-exploration/research/<name>.md
          on the research/notes branch of your fork (made if you have none)
  Review  post-review: posts the review as your pending review on the PR,
          at the commit reviewed, replacing one factory posted before; you
          read, change and submit it on GitHub
  Change  open-pr: opens the branch the fix pushed to your fork as a draft
          PR, unless it has one open already, and aliases the fix's
          sandbox to the PR
          post-replies: posts a revise's replies on the PR, each in the
          thread of the review comment it answers, or quoting the
          conversation comment it answers, and its report as a PR comment

--action does one action the result offers:
  label, comment  that write alone
  push-notes      that write alone
  post-review     that write alone
  open-pr         that write alone
  post-replies    that write alone
  run             the follow-up it offers (run:fix names it): for a Plan,
                  writes the plan, as edited, to the issue's sandbox and
                  runs factory recipe fix --with-plan true there
  revise          one of the recipe's revises (revise:plan names it):
                  factory recipe revise, into the conversation the result
                  came from; it writes a new result and posts nothing

edit and reject are for whoever keeps the result as a draft: edit the
file before applying it, or don't apply it.

Applying the same task's output again does not comment again, nor post
a review that was submitted, nor open a second PR, nor reply twice.`,
		Example: `  factory sandbox task output fix-repo-123 | factory apply -f - --dry-run
  factory sandbox task output fix-repo-123 > triage.yaml   # look, edit
  factory apply -f triage.yaml
  factory apply -f plan.yaml --action comment
  factory apply -f plan.yaml --action run:fix
  factory apply -f plan.yaml --action revise:plan`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			var data []byte
			var err error
			if file == "-" {
				data, err = io.ReadAll(os.Stdin)
			} else {
				data, err = os.ReadFile(file)
			}
			if err != nil {
				return fmt.Errorf("reading %s: %w", file, err)
			}
			docs, err := taskoutput.Parse(data)
			if err != nil {
				return err
			}
			verb, run, _ := strings.Cut(action, ":")
			if action != "" {
				class, ok := taskoutput.VerbClass(verb)
				if !ok {
					return fmt.Errorf("--action %q is not one of: %s", verb, strings.Join(taskoutput.KnownVerbs(), ", "))
				}
				if class == taskoutput.ClassDraft {
					return fmt.Errorf("%s is for whoever keeps the result as a draft: edit the file before applying it, or don't apply it", verb)
				}
				for _, d := range docs {
					if _, err := d.Offer(verb, run); err != nil {
						return fmt.Errorf("%s: %w", d.Target.URL, err)
					}
				}
				if verb == "revise" {
					return runRevises(ctx, c, docs, run, dryRun)
				}
				if class == taskoutput.ClassFollowUp {
					return runFollowUps(ctx, c, docs, run, dryRun)
				}
			}
			gh, err := github.NewClient(ctx)
			if err != nil {
				return fmt.Errorf("creating github client: %w", err)
			}
			for _, d := range docs {
				target := d.Target.URL
				if err := applyDocument(ctx, gh, d, verb, "", dryRun); err != nil {
					return fmt.Errorf("applying the %s for %s: %w", d.Kind, target, err)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&file, "filename", "f", "", "Task output file, or - for stdin")
	cmd.Flags().StringVar(&action, "action", "", "Do one action the task output offers (label, comment, push-notes, post-review, open-pr, post-replies, run[:<follow-up>], revise[:<revise>]) instead of all its writes")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print what would be written, and write nothing")
	_ = cmd.MarkFlagRequired("filename")
	return cmd
}

// applyDocument applies d with gh: each write it offers, or verb's alone.
// A Change's open-pr points d at the PR it opened or found, and the
// sandbox the fix ran in — sandboxName, else the one d names, else its
// target's — is then aliased to that PR, which is how the board and the
// watch find a fix's PR.
func applyDocument(ctx context.Context, gh *githubv39.Client, d *taskoutput.Document, verb, sandboxName string, dryRun bool) error {
	before := d.Target.URL
	var err error
	if verb == "" {
		err = taskoutput.Apply(ctx, gh, d, dryRun, os.Stdout)
	} else {
		err = taskoutput.ApplyAction(ctx, gh, d, verb, dryRun, os.Stdout)
	}
	if err != nil || dryRun || d.Kind != "Change" {
		return err
	}
	pr, err := parseGitHubItemURL(d.Target.URL)
	if err != nil || !pr.IsPR {
		return nil // no PR: open-pr was not among the actions done
	}
	if sandboxName == "" {
		sandboxName = d.Source.Sandbox
	}
	if sandboxName == "" {
		if sandboxName, err = findSandbox(ctx, before); err != nil {
			return fmt.Errorf("%s is open, but its sandbox is not aliased to it: %w", d.Target.URL, err)
		}
	}
	if err := aliasSandbox(ctx, sandboxName, pr.Number, d.Target.URL); err != nil {
		return fmt.Errorf("%s is open, but its sandbox is not aliased to it (applying again does): %w", d.Target.URL, err)
	}
	fmt.Printf("Sandbox %s is %s's\n", sandboxName, d.Target.URL)
	return nil
}

// findSandbox and aliasSandbox are applyDocument's Kubernetes calls.
var (
	findSandbox  = sandboxForURL
	aliasSandbox = func(ctx context.Context, sandboxName string, prNum int, prURL string) error {
		kubeClient, err := clients.NewKubernetesClient()
		if err != nil {
			return fmt.Errorf("creating k8s client: %w", err)
		}
		return factorysandbox.AliasSandboxToPR(ctx, kubeClient, rootFlags.Namespace, sandboxName, prNum, prURL)
	}
)

// runFollowUps starts the follow-up each document offers: for a Plan, a
// fix that follows it.
func runFollowUps(ctx context.Context, c *cobra.Command, docs []*taskoutput.Document, run string, dryRun bool) error {
	if _, err := ResolveRootFlags(c); err != nil {
		return err
	}
	for _, d := range docs {
		a, _ := d.Offer("run", run)
		if a.Run != "fix" {
			return fmt.Errorf("%s: follow-up %q is not one this factory runs", d.Target.URL, a.Run)
		}
		if err := fixWithPlan(ctx, d, dryRun); err != nil {
			return fmt.Errorf("%s: %w", d.Target.URL, err)
		}
	}
	return nil
}

// runRevises runs the revise each document offers, in the conversation of
// the task it came from: in the sandbox it names, or else its target's.
func runRevises(ctx context.Context, c *cobra.Command, docs []*taskoutput.Document, revise string, dryRun bool) error {
	for _, d := range docs {
		a, _ := d.Offer("revise", revise)
		task := d.Source.Session
		if task == "" {
			task = d.Source.Task
		}
		if task == "" {
			return fmt.Errorf("%s: the %s names no task to revise", d.Target.URL, d.Kind)
		}
		where := d.Source.Sandbox
		if where == "" {
			where = d.Target.URL
		}
		if dryRun {
			fmt.Printf("Would run: factory recipe revise %s %s --task %s\n", where, a.Revise, task)
			continue
		}
		if err := runRevise(ctx, c, where, a.Revise, reviseFlags{task: task}); err != nil {
			return fmt.Errorf("%s: %w", d.Target.URL, err)
		}
	}
	return nil
}

// fixWithPlan writes a Plan, as it may have been edited, where a fix
// reads it in the issue's sandbox, and runs the fix recipe there, with
// the plan.
func fixWithPlan(ctx context.Context, d *taskoutput.Document, dryRun bool) error {
	p, err := d.PlanSpec()
	if err != nil {
		return err
	}
	it, err := parseGitHubItemURL(d.Target.URL)
	if err != nil {
		return err
	}
	if it.IsPR {
		return fmt.Errorf("a Plan targets an issue, not %s", d.Target.URL)
	}
	planPath := tasks.PlanFilePath(it.Number)
	if dryRun {
		fmt.Printf("Would write the plan to %s in %s's sandbox and run: factory recipe fix --url %s --with-plan true\n", planPath, d.Target.URL, d.Target.URL)
		return nil
	}
	if err := writePlan(ctx, d.Target.URL, planPath, []byte(strings.TrimSpace(p.Markdown)+"\n")); err != nil {
		return err
	}
	return runFixRecipe(ctx, d.Target.URL)
}

// writePlan and runFixRecipe are fixWithPlan's sandbox calls.
var (
	writePlan = func(ctx context.Context, issueURL, planPath string, plan []byte) error {
		name, err := sandboxForURL(ctx, issueURL)
		if err != nil {
			return err
		}
		client, err := envd.Connect(ctx, rootFlags.Namespace, name)
		if err != nil {
			return fmt.Errorf("connecting to sandbox %s: %w", name, err)
		}
		if err := client.WriteFile(ctx, planPath, plan); err != nil {
			return fmt.Errorf("writing the plan to %s in %s: %w", planPath, name, err)
		}
		fmt.Printf("Wrote the plan to %s in %s; running the fix\n", planPath, name)
		return nil
	}
	runFixRecipe = func(ctx context.Context, issueURL string) error {
		return runRecipe(ctx, "fix", issueURL, "", "", applyMode{}, map[string]string{"with_plan": "true"}, nil, reviseRun{})
	}
)
