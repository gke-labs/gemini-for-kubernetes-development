package commands

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/fanout"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
)

// NewFanoutCommand groups the fan-out's commands: one task, many items,
// child issues labelled for the coder bots a few at a time.
func NewFanoutCommand(ctx context.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fanout",
		Short: "Fan an issue's task out to a child issue per item, in a slow start",
		Long: `Fan an issue's task out to a child issue per item.

The parent issue's spec has the sections ## Task (what to do for one
{item}), ## Items (a checklist), and optionally ## Finally (one more child
once every item is done) and ## Fan-out (settings, a YAML block). The spec
is the newest comment marked <!-- factory:fanout-spec --> by you or a
maintainer, else the issue body.

See factory/design/fanout.md.`,
	}
	cmd.AddCommand(newFanoutSyncCommand(ctx))
	return cmd
}

func newFanoutSyncCommand(ctx context.Context) *cobra.Command {
	var rawURL, trigger string
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "sync --url <issue>",
		Short: "Run one fan-out pass over a parent issue",
		Long: `Run one fan-out pass over a parent issue, as the watch daemon will.

A pass reads the spec, the children (found by their marker) and the state
kept in the progress comment, then:
  - moves the window: +1 per child closed as completed, halved per PR
    closed unmerged, up to window.max
  - does nothing more while the parent has the stop label
  - stops at a checkpoint: adds the stop label, and comments what is done
  - creates the children (create: all) and rewrites the ones not yet
    labelled from the spec
  - labels the next children in the window with the trigger label and the
    spec's labels
  - once every item is done, creates the Finally child, and when that one
    is closed (or there is none), closes the parent
  - updates the progress comment

With --dry-run it writes nothing, and prints what it would write. On a
stopped parent it also prints what the next pass would do once the stop
label is removed.`,
		Example: `  factory fanout sync --url https://github.com/owner/repo/issues/123 --dry-run
  factory fanout sync --url https://github.com/owner/repo/issues/123`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			item, err := parseGitHubItemURL(rawURL)
			if err != nil {
				return err
			}
			if item.IsPR {
				return fmt.Errorf("%s is a pull request; a fan-out parent is an issue", rawURL)
			}
			gh, err := github.NewClient(ctx)
			if err != nil {
				return err
			}
			client := github.ForRepo(gh, item.Owner, item.Repo)
			login, err := client.AuthenticatedLogin(ctx)
			if err != nil {
				return err
			}
			out := c.OutOrStdout()
			res, err := fanout.Sync(ctx, client, fanout.SyncOptions{
				Issue:        item.Number,
				TriggerLabel: trigger,
				BotLogin:     login,
				DryRun:       dryRun,
				Logf:         func(format string, args ...any) { fmt.Fprintf(out, format+"\n", args...) },
			})
			if err != nil {
				return err
			}
			switch {
			case res.Closed:
				fmt.Fprintf(out, "#%d is closed: nothing to do\n", item.Number)
			case res.NoSpec:
				fmt.Fprintln(out, "Write a spec: a comment marked <!-- factory:fanout-spec --> with ## Task and ## Items, or those sections in the issue body.")
			case res.SpecError != nil:
				return fmt.Errorf("the spec does not parse: %w", res.SpecError)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&rawURL, "url", "", "the parent issue's URL")
	cmd.Flags().StringVar(&trigger, "trigger-label", "overseer", "the label that hands a child to the coder bots")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "write nothing; print what the pass would write")
	_ = cmd.MarkFlagRequired("url")
	return cmd
}
