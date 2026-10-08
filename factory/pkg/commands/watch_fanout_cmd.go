package commands

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/config"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/fanout"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
)

// newWatchFanoutCommand runs one fan-out pass over a parent issue: what the
// watch daemon does for every parent labelled <trigger>/fanout, by hand.
func newWatchFanoutCommand(ctx context.Context) *cobra.Command {
	var rawURL, trigger string
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "fanout --url <issue>",
		Short: "Run one fan-out pass over a parent issue",
		Long: `Run one fan-out pass over a parent issue, as the watch daemon will.

A fan-out is one task for many items: a child issue per item, labelled for
the coder bots a few at a time. The parent's spec has the sections ## Task
(a Go template for one item, {{.item.name}}), ## Items (a checklist, or
items.from a JSON file in ## Fan-out), and optionally
## Finally (one more child once every item is done) and ## Fan-out
(settings, a YAML block). The spec is the newest comment marked
<!-- factory:fanout-spec --> by you or a maintainer, else the issue body.
See factory/design/fanout.md.

A pass reads the spec, the children (found by their marker) and the state
kept in the progress comment, then:
  - moves the ramp: a child closed as completed doubles the group (items
    per child) up to group.max, then adds 1 to the window up to
    window.max; a PR closed unmerged halves the window, then the group
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
		Example: `  factory watch fanout --url https://github.com/owner/repo/issues/123 --dry-run
  factory watch fanout --url https://github.com/owner/repo/issues/123`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			item, err := parseGitHubItemURL(rawURL)
			if err != nil {
				return err
			}
			if item.IsPR {
				return fmt.Errorf("%s is a pull request; a fan-out parent is an issue", rawURL)
			}
			if trigger == "" {
				trigger = watchTriggerLabel()
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
				fmt.Fprintln(out, "Write a spec: a comment marked <!-- factory:fanout-spec --> with ## Task and ## Items, or those sections in the issue body; or have an agent propose one: factory recipe fanout --url <issue> --apply. The watch daemon proposes one itself.")
			case res.SpecError != nil:
				return fmt.Errorf("the spec does not parse: %w", res.SpecError)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&rawURL, "url", "", "the parent issue's URL")
	cmd.Flags().StringVar(&trigger, "trigger-label", "", "the label that hands a child to the coder bots (default: the factory config's triggerLabel, as watch uses)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "write nothing; print what the pass would write")
	_ = cmd.MarkFlagRequired("url")
	return cmd
}

// watchTriggerLabel is the trigger label factory watch uses: the factory
// config's, else "factory".
func watchTriggerLabel() string {
	if cfg, err := config.LoadConfig(); err == nil && cfg != nil && cfg.TriggerLabel != "" {
		return cfg.TriggerLabel
	}
	return "factory"
}
