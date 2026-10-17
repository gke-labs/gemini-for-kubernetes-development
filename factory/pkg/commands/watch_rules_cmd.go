package commands

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/watch/rules"
)

// newWatchRulesCommand lists the watch's rules: factory's prebaked ones,
// what today's watch does written as rules (factory/design/watch-rules.md).
func newWatchRulesCommand() *cobra.Command {
	var repoURL, dir, output string
	cmd := &cobra.Command{
		Use:   "rules [--dir <checkout>] [--url <repo>]",
		Short: "List the watch's rules: what each looks at, runs, and as whom",
		Long: `List the watch's rules: factory's prebaked rules, one per file, each a
condition on an issue, a PR, a schedule or an event, and the action taken
on it (a recipe run, or GitHub writes). A group's rules are ordered and
exclusive on an item: the first that holds is taken, the last holds when
no other does. See factory/design/watch-rules.md.

A repository's rules, .agents/rules/<name>.yaml, lie over factory's: a
file of a factory rule's name replaces it, or, with nothing but its name
and enabled, turns it on or off:

  name: review
  enabled: false

--dir lists factory's rules with a local checkout's laid over them, so a
repository's owner sees what their files change (SOURCE: whose rule it
is; ENABLED says "(repo)" when the repository's toggle set it).

Nothing evaluates these rules yet: today's watch still runs its scanners.
--url is accepted for a repository's own rules, which the watch reads
later; today it lists factory's.

Policy floors: give-up and attempts at most 10, a cooldown at least 1m.`,
		Example: `  factory watch rules
  factory watch rules -o yaml
  factory watch rules --dir .`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			set, err := rules.Builtin()
			if err != nil {
				return err
			}
			if dir != "" {
				if set, err = overlayRepoRules(set, dir); err != nil {
					return err
				}
			}
			return printWatchRules(c.OutOrStdout(), set, output)
		},
	}
	cmd.AddCommand(newWatchRulesReplayCommand())
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output format: yaml or json (the whole rules)")
	cmd.Flags().StringVar(&dir, "dir", "", "A local checkout whose .agents/rules/ to lay over factory's rules")
	cmd.Flags().StringVar(&repoURL, "url", "", "A repository URL (its own rules come later; factory's are listed)")
	return cmd
}

// newWatchRulesReplayCommand runs the rules on what today's scanners
// recorded (the watch's --rules-record) and lists where they decide
// differently.
func newWatchRulesReplayCommand() *cobra.Command {
	var dir, output string
	var show int
	cmd := &cobra.Command{
		Use:   "replay <decisions.jsonl>...",
		Short: "Replay recorded scanner decisions through the rules and list the differences",
		Long: `Replay the decisions today's issue and PR scanners recorded (factory watch
--rules-record, the Overseer's spec.rulesRecord: decisions.jsonl on the
overseer's volume) through the rules: each record is the facts an item was
decided on and what the scanner did; the rules decide on the same facts,
and every record where they do something else is listed. A record a gate
outside the rules held back (a busy sandbox, one needing a person) is
counted, not compared.

--dir lays a checkout's .agents/rules/ over factory's rules first, as
'factory watch rules --dir' does.`,
		Example: `  kubectl exec -n overseer-kcc overseer-kcc -- cat /workspaces/.cache/factory-watch/decisions.jsonl > decisions.jsonl
  factory watch rules replay decisions.jsonl
  factory watch rules replay decisions.jsonl -o json`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			set, err := rules.Builtin()
			if err != nil {
				return err
			}
			if dir != "" {
				if set, err = overlayRepoRules(set, dir); err != nil {
					return err
				}
			}
			total := rules.ReplayResult{ByRule: map[string]int{}}
			for _, path := range args {
				f, err := os.Open(path)
				if err != nil {
					return err
				}
				res, err := set.Replay(f)
				f.Close()
				if err != nil {
					return fmt.Errorf("%s: %w", path, err)
				}
				total.Compared += res.Compared
				total.Gated += res.Gated
				total.Diffs = append(total.Diffs, res.Diffs...)
				for r, n := range res.ByRule {
					total.ByRule[r] += n
				}
			}
			return printReplay(c.OutOrStdout(), total, output, show)
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "", "A local checkout whose .agents/rules/ to lay over factory's rules")
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output format: json (every difference, with its record)")
	cmd.Flags().IntVar(&show, "show", 20, "How many differences to list")
	return cmd
}

// printReplay writes the replay's summary, the differences by rule and the
// first differences, or all of it as JSON.
func printReplay(out io.Writer, res rules.ReplayResult, output string, show int) error {
	switch output {
	case "json":
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	case "":
	default:
		return fmt.Errorf("unknown output format %q (want json)", output)
	}
	fmt.Fprintf(out, "%d decisions compared, %d agree, %d differ; %d held back by a gate outside the rules.\n",
		res.Compared, res.Compared-len(res.Diffs), len(res.Diffs), res.Gated)
	if len(res.Diffs) == 0 {
		return nil
	}
	ruleNames := make([]string, 0, len(res.ByRule))
	for r := range res.ByRule {
		ruleNames = append(ruleNames, r)
	}
	sort.Slice(ruleNames, func(i, j int) bool {
		if res.ByRule[ruleNames[i]] != res.ByRule[ruleNames[j]] {
			return res.ByRule[ruleNames[i]] > res.ByRule[ruleNames[j]]
		}
		return ruleNames[i] < ruleNames[j]
	})
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "\nRULE\tDIFFERS")
	for _, r := range ruleNames {
		fmt.Fprintf(w, "%s\t%d\n", r, res.ByRule[r])
	}
	fmt.Fprintln(w, "\nAT\tITEM\tRULES\tSCANNER")
	for i, d := range res.Diffs {
		if i == show {
			fmt.Fprintf(w, "… %d more (-o json for all)\t\t\t\n", len(res.Diffs)-show)
			break
		}
		fmt.Fprintf(w, "%s\t%s#%d\t%s\t%s\n", d.Record.At.UTC().Format(time.RFC3339), d.Record.Item.Kind, d.Record.Item.Number,
			orDash(strings.Join(d.Engine, ", ")), orDash(strings.Join(d.Scanner, ", ")))
	}
	return w.Flush()
}

// overlayRepoRules lays the rules in a checkout's .agents/rules/ over set.
// A checkout without the directory has no rules of its own.
func overlayRepoRules(set rules.Set, checkout string) (rules.Set, error) {
	dir := filepath.Join(checkout, ".agents", "rules")
	if info, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return set, nil
	} else if err != nil {
		return nil, err
	} else if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", dir)
	}
	repo, err := rules.Load(os.DirFS(dir))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", dir, err)
	}
	return rules.Overlay(set, repo)
}

// printWatchRules writes the rules as a table, or whole as YAML or JSON.
func printWatchRules(out io.Writer, set rules.Set, output string) error {
	ordered := orderWatchRules(set)
	switch output {
	case "json":
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(ordered)
	case "yaml":
		enc := yaml.NewEncoder(out)
		enc.SetIndent(2)
		if err := enc.Encode(ordered); err != nil {
			return err
		}
		return enc.Close()
	case "":
	default:
		return fmt.Errorf("unknown output format %q (want yaml or json)", output)
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tON\tFROM\tGROUP\tRUN\tAS\tPOLICY\tSOURCE\tENABLED")
	for _, r := range ordered {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.Name, r.On, orDash(strings.Join(r.From, ",")), ruleGroup(r), ruleRun(r), orDash(r.As), rulePolicy(r), orDash(r.Source), ruleEnabled(r))
	}
	return w.Flush()
}

var ruleOnOrder = map[string]int{rules.OnIssue: 0, rules.OnPR: 1, rules.OnSchedule: 2, rules.OnEvent: 3}

// orderWatchRules orders the rules by what they look at; within that, the
// rules of no group by name, then each group's rules in the group's order.
func orderWatchRules(set rules.Set) []*rules.Rule {
	groups := set.Groups()
	var out []*rules.Rule
	for _, r := range set {
		if r.Group == nil {
			out = append(out, r)
		}
	}
	names := make([]string, 0, len(groups))
	for name := range groups {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		out = append(out, groups[name]...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if ruleOnOrder[a.On] != ruleOnOrder[b.On] {
			return ruleOnOrder[a.On] < ruleOnOrder[b.On]
		}
		if (a.Group == nil) != (b.Group == nil) {
			return a.Group == nil
		}
		if a.Group == nil {
			return a.Name < b.Name
		}
		return a.Group.Name < b.Group.Name
	})
	return out
}

// ruleEnabled is the rule's enabled, and whose it is when a repository's
// toggle set it.
func ruleEnabled(r *rules.Rule) string {
	if r.EnabledBy != "" && r.EnabledBy != r.Source {
		return fmt.Sprintf("%t (%s)", r.IsEnabled(), r.EnabledBy)
	}
	return fmt.Sprintf("%t", r.IsEnabled())
}

func ruleGroup(r *rules.Rule) string {
	switch {
	case r.Group == nil:
		return "-"
	case r.Group.Last:
		return r.Group.Name + "/last"
	default:
		return fmt.Sprintf("%s/%d", r.Group.Name, r.Group.Order)
	}
}

func ruleRun(r *rules.Rule) string {
	if r.Run.Recipe != "" {
		if r.Run.Apply != "" {
			return r.Run.Recipe + " → " + r.Run.Apply
		}
		return r.Run.Recipe
	}
	var writes []string
	add := func(name string, ok bool) {
		if ok {
			writes = append(writes, name)
		}
	}
	add("label", len(r.Run.Label) > 0)
	add("unlabel", len(r.Run.Unlabel) > 0)
	add("comment", r.Run.Comment != "")
	add("assign", r.Run.Assign != "")
	add("unassign", r.Run.Unassign != "")
	add("stop", r.Run.Stop != nil)
	add("drop", r.Run.Drop)
	return strings.Join(writes, ",")
}

func rulePolicy(r *rules.Rule) string {
	p := r.Policy
	var parts []string
	if p.Dedupe != "" {
		parts = append(parts, "dedupe="+p.Dedupe)
	}
	if c := p.Cooldown; c != nil {
		cd := "cooldown=" + c.Success.String()
		if c.Failure.IsSet() {
			cd += "/" + c.Failure.String()
		}
		if len(c.ResetOn) > 0 {
			cd += "(reset:" + strings.Join(c.ResetOn, ",") + ")"
		}
		parts = append(parts, cd)
	}
	if g := p.GiveUp; g != nil {
		gu := fmt.Sprintf("give-up=%d", g.Attempts)
		if len(g.Since) > 0 {
			gu += "/" + g.Since.String()
		}
		parts = append(parts, gu)
	}
	if p.Before != "" {
		parts = append(parts, "before="+p.Before)
	}
	if p.SkipCooldown {
		parts = append(parts, "skip-cooldown")
	}
	if p.Phase != 0 {
		parts = append(parts, fmt.Sprintf("phase=%d", p.Phase))
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, " ")
}
