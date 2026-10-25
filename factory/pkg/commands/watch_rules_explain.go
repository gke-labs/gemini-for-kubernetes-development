package commands

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/concurrency"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/issues"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/prs"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/sandbox"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/watch/rules"
)

// explainOptions are the overseer's settings the facts are read with.
type explainOptions struct {
	trigger, watcher       string
	pool, reviewers, trust []string
	inactivity             time.Duration
	records                []string
	dir, output            string
	verbose                bool
}

// newWatchRulesExplainCommand walks one PR or issue through the rules and
// says, rule by rule, what each decides or why it does not.
func newWatchRulesExplainCommand() *cobra.Command {
	var o explainOptions
	cmd := &cobra.Command{
		Use:   "explain <PR or issue URL>",
		Short: "Walk one PR or issue through the rules, rule by rule",
		Long: `Walk one PR or issue through the watch's rules, as the engine does, and
say for each rule what it decides, or why not: off, a task of the item
active, an earlier rule took it, the predicates of its when that fail (with
what the item has), or what its policy holds it back with.

The facts are read as today's scanner reads them, live and read-only (the
GitHub client refuses every write), with the overseer's settings given by
the flags. The run history behind cooldowns, give-ups and dedupe is the
overseer's and is not known here: --record reads the facts from the
overseer's own last record of the item instead (decisions.jsonl, the
Overseer's spec.rulesRecord), history and settings included:

  kubectl exec -n overseer-kcc overseer-kcc -- cat /workspaces/.cache/factory-watch/decisions.jsonl > decisions.jsonl
  factory watch rules explain https://github.com/o/r/pull/7 --record decisions.jsonl`,
		Example: `  factory watch rules explain https://github.com/o/r/pull/7 --pool bot-a,bot-b --watcher watcher-bot
  factory watch rules explain https://github.com/o/r/issues/12 --record decisions.jsonl
  factory watch rules explain https://github.com/o/r/pull/7 --record decisions.jsonl -o json`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			item, err := parseGitHubItemURL(args[0])
			if err != nil {
				return err
			}
			set, err := rules.Builtin()
			if err != nil {
				return err
			}
			if o.dir != "" {
				if set, err = overlayRepoRules(set, o.dir); err != nil {
					return err
				}
			}
			var rec *rules.Record
			source := ""
			if len(o.records) > 0 {
				if rec, err = lastRecordOf(o.records, item); err != nil {
					return err
				}
				source = "the overseer's record of " + rec.At.UTC().Format(time.RFC3339)
			} else {
				if rec, err = recordLive(c.Context(), item, o); err != nil {
					return err
				}
				source = "read live (read-only); run history unknown: --record gives the overseer's"
			}
			env := rec.Env
			if env.Now.IsZero() {
				env.Now = rec.At
			}
			ex := set.Explain(rec.Item, env)
			if o.output == "json" {
				enc := json.NewEncoder(c.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(struct {
					Record      *rules.Record     `json:"record"`
					Explanation rules.Explanation `json:"explanation"`
				}{rec, ex})
			}
			if o.output != "" {
				return fmt.Errorf("unknown output format %q (want json)", o.output)
			}
			printExplanation(c.OutOrStdout(), item, rec, set, ex, source)
			return nil
		},
	}
	cmd.Flags().StringVar(&o.trigger, "trigger", "overseer", "The overseer's trigger label")
	cmd.Flags().StringVar(&o.watcher, "watcher", "", "The watcher's own login")
	cmd.Flags().StringSliceVar(&o.pool, "pool", nil, "The bot pool's logins (author: pool, assignee: pool)")
	cmd.Flags().StringSliceVar(&o.reviewers, "reviewers", nil, "The logins whose reviews are the automated review")
	cmd.Flags().StringSliceVar(&o.trust, "trusted", nil, "Logins trusted whatever GitHub's association says")
	cmd.Flags().DurationVar(&o.inactivity, "inactivity", 0, "The overseer's prInactivityTimeout (0: never)")
	cmd.Flags().StringSliceVar(&o.records, "record", nil, "decisions.jsonl files, or the record's directory: explain the overseer's last record of the item instead of reading it live")
	cmd.Flags().StringVar(&o.dir, "dir", "", "A local checkout whose .agents/watch/ to lay over factory's rules")
	cmd.Flags().StringVarP(&o.output, "output", "o", "", "Output format: json (the record and the explanation)")
	cmd.Flags().BoolVarP(&o.verbose, "verbose", "v", false, "Show the scanner's own log while it reads the facts")
	return cmd
}

// recordLive reads the item's facts as the dispatcher will at start
// (Scanner.Facts: read-only, with a read-only client, every fact, not
// stopping at an active task), and returns them as a record.
func recordLive(ctx context.Context, item githubItem, o explainOptions) (*rules.Record, error) {
	gh, err := github.NewReadOnlyClient(ctx)
	if err != nil {
		return nil, err
	}
	repo := github.ForRepo(gh, item.Owner, item.Repo)
	dir, err := os.MkdirTemp("", "factory-explain-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	queue := concurrency.NewTaskQueueManager(concurrency.TaskQueueManagerConfig{QueueDir: dir})
	if err := queue.EnsureDirs(); err != nil {
		return nil, err
	}
	sandboxes := sandbox.NewService(sandbox.ServiceConfig{Owner: item.Owner, Repo: item.Repo}, sandbox.ServiceDeps{GitHub: gh})
	trusted := append(append([]string(nil), o.trust...), o.reviewers...)

	// The scanners narrate what they read; that is not the answer.
	restore := quiet(o.verbose)
	defer restore()
	var it rules.Item
	var env rules.Env
	scanner := "issues"
	if item.IsPR {
		scanner = "prs"
		s := prs.New(prs.Config{
			TriggerLabel: o.trigger, GitHubLogin: o.watcher, BotUsers: o.pool, ReviewerLogins: o.reviewers,
			TrustedLogins: trusted, InactivityTimeout: o.inactivity, DryRun: true,
		}, prs.Deps{GitHub: repo, Queue: queue, Entities: concurrency.NewEntityStateCache(), Sandboxes: sandboxes})
		it, env, err = s.Facts(ctx, item.Number)
	} else {
		s := issues.New(issues.Config{
			TriggerLabel: o.trigger, GitHubLogin: o.watcher, BotUsers: o.pool, TrustedLogins: trusted, DryRun: true,
		}, issues.Deps{GitHub: repo, Queue: queue, Entities: concurrency.NewEntityStateCache(), Sandboxes: sandboxes})
		it, env, err = s.Facts(ctx, item.Number)
	}
	if err != nil {
		return nil, err
	}
	return &rules.Record{At: env.Now, Scanner: scanner, Env: env, Item: it}, nil
}

// quiet sends the process's stdout and stderr nowhere until restored,
// unless verbose.
func quiet(verbose bool) func() {
	if verbose {
		return func() {}
	}
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return func() {}
	}
	stdout, stderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = null, null
	return func() {
		os.Stdout, os.Stderr = stdout, stderr
		null.Close()
	}
}

// lastRecordOf is the newest record of the item in the files.
func lastRecordOf(paths []string, item githubItem) (*rules.Record, error) {
	kind := rules.OnIssue
	if item.IsPR {
		kind = rules.OnPR
	}
	var files []string
	for _, p := range paths {
		fs, err := rules.RecordFiles(p)
		if err != nil {
			return nil, err
		}
		files = append(files, fs...)
	}
	var last *rules.Record
	for _, path := range files {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 16<<20)
		for sc.Scan() {
			line := sc.Bytes()
			if len(line) == 0 {
				continue
			}
			var rec rules.Record
			if err := json.Unmarshal(line, &rec); err != nil {
				continue
			}
			if rec.Item.Kind == kind && rec.Item.Number == item.Number && (last == nil || rec.At.After(last.At)) {
				r := rec
				last = &r
			}
		}
		f.Close()
		if err := sc.Err(); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	if last == nil {
		return nil, fmt.Errorf("no record of %s #%d in %s", kind, item.Number, strings.Join(paths, ", "))
	}
	return last, nil
}

// printExplanation writes the item's facts and the walk, rule by rule.
func printExplanation(out io.Writer, item githubItem, rec *rules.Record, set rules.Set, ex rules.Explanation, source string) {
	kind := "issue"
	if item.IsPR {
		kind = "PR"
	}
	fmt.Fprintf(out, "%s %s/%s#%d  (facts %s)\n", kind, item.Owner, item.Repo, item.Number, source)
	fmt.Fprintf(out, "facts   %s\n", factsLine(rec.Item))
	if rec.Gate != "" {
		fmt.Fprintf(out, "note    today's scanner was held back by a gate outside the rules: %s\n", rec.Gate)
	}
	if ex.Gate != "" {
		fmt.Fprintf(out, "gate    the item %s: no rule decides\n", ex.Gate)
	}
	fmt.Fprintln(out)
	printSteps(out, set, ex)
}

// printSteps writes the walk, rule by rule, and what it decides.
func printSteps(out io.Writer, set rules.Set, ex rules.Explanation) {
	order := map[string]int{}
	for _, r := range set {
		order[r.Name] = r.Order
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	for _, st := range ex.Steps {
		mark := " "
		if st.Exclusive {
			mark = "●"
		}
		place := "   "
		if n := order[st.Rule]; n > 0 {
			place = fmt.Sprintf("%3d", n)
		}
		var sign, what string
		switch {
		case st.Decision != nil:
			sign, what = "✓", decisionText(*st.Decision)
			if st.Ends {
				what += "  → the walk ends"
			}
		case st.Skipped != "":
			sign, what = "–", "skipped: "+st.Skipped
		case len(st.Failed) > 0:
			sign, what = "✗", strings.Join(st.Failed, "; ")
		case st.Held != "":
			sign, what = "⏸", "held: "+st.Held
		}
		fmt.Fprintf(w, "%s %s %s\t%s\t%s\n", place, mark, st.Rule, sign, what)
	}
	w.Flush()
	fmt.Fprintf(out, "\ndecides %s\n", strings.Join(decisionList(ex), ", "))
}

// decisionList is what an explanation decides, "<rule> <outcome>" each, or
// "nothing".
func decisionList(ex rules.Explanation) []string {
	var decided []string
	for _, d := range ex.Decisions() {
		decided = append(decided, d.Rule+" "+d.Outcome)
	}
	if len(decided) == 0 {
		decided = []string{"nothing"}
	}
	return decided
}

func decisionText(d rules.Decision) string {
	if d.Outcome == rules.OutcomeGiveUp {
		return fmt.Sprintf("give up: the stop label + %q", d.Writes.Comment)
	}
	if d.Recipe != "" {
		s := "run " + d.Recipe
		if d.Apply != "" {
			s += " → " + d.Apply
		}
		return s
	}
	var parts []string
	if len(d.Writes.Label) > 0 {
		parts = append(parts, "label "+strings.Join(d.Writes.Label, ", "))
	}
	if len(d.Writes.Unlabel) > 0 {
		parts = append(parts, "unlabel "+strings.Join(d.Writes.Unlabel, ", "))
	}
	if len(d.Writes.Assign) > 0 {
		parts = append(parts, "assign "+strings.Join(d.Writes.Assign, ", "))
	}
	if len(d.Writes.Unassign) > 0 {
		parts = append(parts, "unassign "+strings.Join(d.Writes.Unassign, ", "))
	}
	if d.Writes.Stop {
		parts = append(parts, fmt.Sprintf("the stop label + %q", d.Writes.Comment))
	} else if d.Writes.Comment != "" {
		parts = append(parts, fmt.Sprintf("comment %q", d.Writes.Comment))
	}
	if d.Writes.Drop {
		parts = append(parts, "drop the queued tasks")
	}
	return strings.Join(parts, "; ")
}

// factsLine is the item's facts that the rules read, on one line.
func factsLine(it rules.Item) string {
	author := "author " + orDash(it.Author)
	if it.AuthorAssociation != "" {
		author += " (" + strings.ToLower(it.AuthorAssociation) + ")"
	}
	parts := []string{author}
	if len(it.Labels) > 0 {
		parts = append(parts, "labels "+strings.Join(it.Labels, ", "))
	}
	if len(it.Assignees) > 0 {
		parts = append(parts, "assigned "+strings.Join(it.Assignees, ", "))
	}
	if it.Kind == rules.OnPR {
		parts = append(parts, "head "+shortHead(it.Head), "checks "+orDash(it.Checks))
		if it.Mergeable != nil {
			parts = append(parts, fmt.Sprintf("mergeable %t", *it.Mergeable))
		}
		parts = append(parts, fmt.Sprintf("new feedback %t", it.NewTrustedFeedback), fmt.Sprintf("reviewed at head %t", it.ReviewedAtHead))
		if it.Approved {
			parts = append(parts, "approved")
		}
		if it.Draft {
			parts = append(parts, "draft")
		}
	} else {
		parts = append(parts, "linked PR "+orDash(it.LinkedPR), "workflow "+orDash(it.Workflow))
		if it.FanoutParent {
			parts = append(parts, "fan-out parent")
		}
		if it.GraphParent {
			parts = append(parts, "graph parent")
		}
	}
	if it.TaskActive {
		parts = append(parts, "a task active")
	}
	if len(it.Queued) > 0 {
		parts = append(parts, "queued "+strings.Join(it.Queued, ", "))
	}
	parts = append(parts, fmt.Sprintf("%d past runs", len(it.Runs)))
	return strings.Join(parts, " · ")
}

func shortHead(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return orDash(sha)
}
