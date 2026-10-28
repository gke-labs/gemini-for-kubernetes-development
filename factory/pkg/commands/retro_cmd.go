package commands

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/retro"
)

// NewRetroCommand is `factory retro`: the weekly retro (design/retro.md §6).
func NewRetroCommand(ctx context.Context) *cobra.Command {
	var rawURL, since, stageOnly, collector string
	cmd := &cobra.Command{
		Use:   "retro --url <repository> [--since 7d] --stage-only <dir>",
		Short: "The weekly retro: stage the week's outcomes, feedback and traces",
		Long: `The weekly retro reads the week's outcome records, rollups, and the
human feedback and archived agent traces of the PRs worth a look from the
token-usage collector, and has the retro recipe propose edits to the
skills, workflows and review instructions behind them (design/retro.md).

--stage-only <dir> stages the week into <dir> and stops: what a run would
analyse, in the layout the retro recipe reads (pkg/retro). A PR is picked
when it closed unmerged, took 3 or more care passes, has human feedback, or
had 20 or more lines changed by a human after the agent's last push.

The collector is COLLECTOR_URL, or --collector.`,
		Example: `  factory retro --url https://github.com/GoogleCloudPlatform/k8s-config-connector --since 7d --stage-only /tmp/retro`,
		RunE: func(c *cobra.Command, args []string) error {
			owner, repo, err := parseGitHubRepoURL(rawURL)
			if err != nil {
				return err
			}
			from, err := parseSince(since, time.Now())
			if err != nil {
				return err
			}
			if stageOnly == "" {
				return fmt.Errorf("give --stage-only <dir>: running the retro recipe on the staged week is not built yet (design/retro.md step 6c)")
			}
			if collector == "" {
				collector = os.Getenv("COLLECTOR_URL")
			}
			st, err := retro.Stage(ctx, retro.Config{Collector: collector, Owner: owner, Repo: repo, Since: from, Dir: stageOnly})
			if err != nil {
				return err
			}
			out := c.OutOrStdout()
			fmt.Fprintf(out, "Staged %s, %s to %s, into %s: %d outcome records, %d PRs picked\n",
				st.Repo, st.Since.Format(time.RFC3339), st.Until.Format(time.RFC3339), stageOnly, st.Outcomes, len(st.PRs))
			for _, p := range st.PRs {
				fmt.Fprintf(out, "  #%d %s: %s (%d tasks, feedback %t)\n", p.PR, p.State, strings.Join(p.Reasons, "; "), len(p.Tasks), p.Feedback)
				for _, s := range p.Skipped {
					fmt.Fprintf(out, "      skipped %s\n", s)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&rawURL, "url", "", "the repository, https://github.com/<owner>/<repo>")
	cmd.Flags().StringVar(&since, "since", "7d", "the window's start: a duration back from now (7d, 36h) or a date (2026-10-20)")
	cmd.Flags().StringVar(&stageOnly, "stage-only", "", "stage the week into this directory and stop")
	cmd.Flags().StringVar(&collector, "collector", "", "the collector's URL (default COLLECTOR_URL)")
	_ = cmd.MarkFlagRequired("url")
	return cmd
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
