package commands

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/retro"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/taskapi"
)

// NewRetroCommand is `factory retro`: the weekly retro (design/retro.md §6).
func NewRetroCommand(ctx context.Context) *cobra.Command {
	var rawURL, since, stageOnly, staged, collector, runName string
	var apply, dryRun bool
	cmd := &cobra.Command{
		Use:   "retro --url <repository> [--since 7d] [--stage-only <dir> | --staged <dir>]",
		Short: "The weekly retro: improve the agents' instructions from the week's PRs",
		Long: `The weekly retro reads the week's outcome records, rollups, and the
human feedback and archived agent traces of the PRs worth a look from the
token-usage collector, and has the retro recipe propose edits to the
skills, workflows and review instructions behind them (design/retro.md).

It stages the week from the collector here, in the caller - the sandbox
never reaches the collector - then runs the retro recipe in the
repository's retro sandbox with the staged files in its task's staged/,
and, unless --apply=false, opens its Change as a PR (open-pr).

A PR is picked when it closed unmerged, took 3 or more care passes, has
human feedback, or had 20 or more lines changed by a human after the
agent's last push.

--stage-only <dir> stages the week into <dir> and stops: what a run would
analyse, in the layout the recipe reads (pkg/retro). --staged <dir> runs
the recipe on a week already staged there, for trying a change to the
recipe against the same week.

The collector is COLLECTOR_URL, or --collector.`,
		Example: `  factory retro --url https://github.com/GoogleCloudPlatform/k8s-config-connector --since 7d --stage-only /tmp/retro
  factory retro --url https://github.com/GoogleCloudPlatform/k8s-config-connector --staged /tmp/retro --apply=false
  factory retro --url https://github.com/GoogleCloudPlatform/k8s-config-connector`,
		RunE: func(c *cobra.Command, args []string) error {
			owner, repo, err := parseGitHubRepoURL(rawURL)
			if err != nil {
				return err
			}
			from, err := parseSince(since, time.Now())
			if err != nil {
				return err
			}
			if stageOnly != "" && staged != "" {
				return fmt.Errorf("give --stage-only or --staged, not both")
			}
			out := c.OutOrStdout()
			dir := staged
			if dir == "" {
				if dir = stageOnly; dir == "" {
					if dir, err = os.MkdirTemp("", "retro-"); err != nil {
						return err
					}
					defer os.RemoveAll(dir)
				}
				if collector == "" {
					collector = os.Getenv("COLLECTOR_URL")
				}
				st, err := retro.Stage(ctx, retro.Config{Collector: collector, Owner: owner, Repo: repo, Since: from, Dir: dir})
				if err != nil {
					return err
				}
				printStaged(out, st, dir)
				if stageOnly != "" {
					return nil
				}
			} else if _, err := os.Stat(filepath.Join(dir, retro.StagedFile)); err != nil {
				return fmt.Errorf("--staged %s holds no staged week: %w", dir, err)
			}
			if runName == "" {
				runName = "retro-" + time.Now().UTC().Format("20060102")
			}
			repoURL := "https://github.com/" + owner + "/" + repo
			return runRecipeIn(ctx, recipeRunOpts{files: &taskapi.Files{Dir: dir, To: "staged"}}, "retro", repoURL, runName, retroSandboxKey, "", false,
				applyMode{on: apply, dryRun: dryRun, verb: "open-pr"}, map[string]string{}, nil)
		},
	}
	cmd.Flags().StringVar(&rawURL, "url", "", "the repository, https://github.com/<owner>/<repo>")
	cmd.Flags().StringVar(&since, "since", "7d", "the window's start: a duration back from now (7d, 36h) or a date (2026-10-20)")
	cmd.Flags().StringVar(&stageOnly, "stage-only", "", "stage the week into this directory and stop")
	cmd.Flags().StringVar(&staged, "staged", "", "run the recipe on the week already staged in this directory")
	cmd.Flags().StringVar(&collector, "collector", "", "the collector's URL (default COLLECTOR_URL)")
	cmd.Flags().StringVar(&runName, "run-name", "", "the run's name (default retro-<today>): a run of the same name is picked up, not started again")
	cmd.Flags().BoolVar(&apply, "apply", true, "open the retro's Change as a PR when it ends")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "with --apply, say what open-pr would do and do nothing")
	_ = cmd.MarkFlagRequired("url")
	return cmd
}

// retroSandboxKey is the one key factory retro runs under: every week's
// retro of a repository shares its retro sandbox, and its checkout.
const retroSandboxKey = "weekly"

// printStaged says what was staged, and what was skipped.
func printStaged(out io.Writer, st *retro.Staged, dir string) {
	fmt.Fprintf(out, "Staged %s, %s to %s, into %s: %d outcome records, %d PRs picked\n",
		st.Repo, st.Since.Format(time.RFC3339), st.Until.Format(time.RFC3339), dir, st.Outcomes, len(st.PRs))
	for _, p := range st.PRs {
		fmt.Fprintf(out, "  #%d %s: %s (%d tasks, feedback %t)\n", p.PR, p.State, strings.Join(p.Reasons, "; "), len(p.Tasks), p.Feedback)
		for _, s := range p.Skipped {
			fmt.Fprintf(out, "      skipped %s\n", s)
		}
	}
}

// parseSince is a window's start: <n>d, a Go duration back from now, or a
// date or RFC 3339 time.
func parseSince(s string, now time.Time) (time.Time, error) {
	if days, ok := strings.CutSuffix(s, "d"); ok {
		if n, err := strconv.Atoi(days); err == nil && n > 0 {
			return now.AddDate(0, 0, -n), nil
		}
	}
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return now.Add(-d), nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("--since %q: want 7d, 36h or a date (2006-01-02)", s)
}
