package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/conventions"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/watch/rules"
)

// scenario is one item, written by hand, for the rules to decide on: its
// facts, the overseer's settings and the time they are decided at, and,
// when it says, what they are expected to decide.
type scenario struct {
	Name string      `json:"name"`
	Env  scenarioEnv `json:"env"`
	Item rules.Item  `json:"item"`
	// Expect is what the rules should decide, "<rule> <outcome>" each, in
	// the order they decide it; empty (expect: []) is nothing. Unset, the
	// scenario is only explained.
	Expect *[]string `json:"expect"`

	source string
}

// scenarioEnv is the overseer's side of a scenario.
type scenarioEnv struct {
	Trigger    string    `json:"trigger"`
	Watcher    string    `json:"watcher"`
	Pool       []string  `json:"pool"`
	PRLabels   []string  `json:"prLabels"`
	Inactivity string    `json:"inactivity"`
	Now        time.Time `json:"now"`
}

// newWatchRulesDecideCommand decides items written by hand: the rules'
// answer to facts that have not happened yet, without GitHub.
func newWatchRulesDecideCommand() *cobra.Command {
	var files []string
	var dir, output, pool, watcher string
	cmd := &cobra.Command{
		Use:   "decide -f <scenario.yaml or directory>...",
		Short: "Decide items written by hand, rule by rule, and check what was expected",
		Long: `Walk items written by hand through the watch's rules, as the engine does,
and print what each rule decides or why not (as factory watch rules
explain does for a real one). No GitHub: the facts are the scenario's.

A scenario file holds one or more YAML documents:

  name: a clean bot review is not feedback
  env:                        # optional; the overseer's side
    trigger: overseer         #   default overseer
    pool: [ada-coder-bot]     #   default --pool
    watcher: argus-watcher-bot
    inactivity: 336h          #   prInactivityTimeout; default 0 (never)
    now: 2026-10-20T12:00:00Z #   default now; set it when runs have times
  item:                       # the facts, as the record writes them
    kind: pr
    number: 14118
    author: ada-coder-bot
    labels: [overseer, overseer/review]
    head: abc1234
    checks: green
    mergeable: true
    newTrustedFeedback: false
    reviewedAtHead: true
    runs:
      - {rule: review, startedAt: 2026-10-20T11:00:00Z, endedAt: 2026-10-20T11:10:00Z, head: abc1234}
  expect: [ready run]         # optional: what the rules should decide; [] is nothing

The item's fields are the record's (decisions.jsonl's "item"), in any
case. With expect, a scenario that decides otherwise fails, and so does
the command: a directory of scenarios is a test suite for the rules.

--dir lays a checkout's .agents/watch/ over factory's rules first, so a
rule being changed is decided with.`,
		Example: `  factory watch rules decide -f scenario.yaml
  factory watch rules decide -f ./scenarios/
  factory watch rules decide -f ./scenarios/ --dir .   # with a checkout's rules`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if len(files) == 0 {
				return errors.New("decide needs -f <scenario.yaml or directory>")
			}
			set, err := rules.Builtin()
			if err != nil {
				return err
			}
			if dir != "" {
				if set, err = overlayRepoRules(set, dir); err != nil {
					return err
				}
			}
			var scenarios []scenario
			for _, f := range files {
				ss, err := loadScenarios(f)
				if err != nil {
					return err
				}
				scenarios = append(scenarios, ss...)
			}
			defaults := scenarioEnv{Trigger: "overseer", Watcher: watcher}
			if pool != "" {
				defaults.Pool = strings.Split(pool, ",")
			}
			return decideScenarios(c.OutOrStdout(), set, scenarios, defaults, output)
		},
	}
	cmd.Flags().StringSliceVarP(&files, "file", "f", nil, "Scenario files or directories of them (*.yaml)")
	cmd.Flags().StringVar(&dir, "dir", "", "A local checkout whose .agents/watch/ to lay over factory's rules")
	cmd.Flags().StringVar(&pool, "pool", "", "The bot pool's logins, for scenarios that name none")
	cmd.Flags().StringVar(&watcher, "watcher", "", "The watcher's login, for scenarios that name none")
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output format: json (each scenario's explanation)")
	return cmd
}

// loadScenarios reads the scenarios a path names: a YAML file of one or
// more documents, or a directory's *.yaml files, by name.
func loadScenarios(path string) ([]scenario, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	files := []string{path}
	if info.IsDir() {
		files, _ = filepath.Glob(filepath.Join(path, "*.yaml"))
		sort.Strings(files)
		if len(files) == 0 {
			return nil, fmt.Errorf("no *.yaml scenarios in %s", path)
		}
	}
	var out []scenario
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		dec := yaml.NewDecoder(bytes.NewReader(data))
		for n := 1; ; n++ {
			var doc any
			if err := dec.Decode(&doc); errors.Is(err, io.EOF) {
				break
			} else if err != nil {
				return nil, fmt.Errorf("%s: document %d: %w", f, n, err)
			}
			if doc == nil {
				continue
			}
			// The record's item is JSON with Go's field names; through JSON,
			// a scenario may write them in any case (kind, newTrustedFeedback).
			b, err := json.Marshal(doc)
			if err != nil {
				return nil, fmt.Errorf("%s: document %d: %w", f, n, err)
			}
			var s scenario
			if err := json.Unmarshal(b, &s); err != nil {
				return nil, fmt.Errorf("%s: document %d: %w", f, n, err)
			}
			if s.Item.Kind == "" {
				return nil, fmt.Errorf("%s: document %d: the item needs a kind (issue, pr, schedule, event)", f, n)
			}
			s.source = fmt.Sprintf("%s#%d", filepath.Base(f), n)
			out = append(out, s)
		}
	}
	return out, nil
}

// scenarioResult is a scenario decided, for -o json.
type scenarioResult struct {
	Name        string            `json:"name"`
	Source      string            `json:"source"`
	Decides     []string          `json:"decides"`
	Expect      *[]string         `json:"expect,omitempty"`
	Pass        *bool             `json:"pass,omitempty"`
	Explanation rules.Explanation `json:"explanation"`
}

// decideScenarios decides each scenario and prints it; it fails when a
// scenario decides other than it expects.
func decideScenarios(out io.Writer, set rules.Set, scenarios []scenario, defaults scenarioEnv, output string) error {
	if output != "" && output != "json" {
		return fmt.Errorf("unknown output format %q (want json)", output)
	}
	var results []scenarioResult
	failed := 0
	for i, s := range scenarios {
		env, err := s.env(defaults)
		if err != nil {
			return fmt.Errorf("%s: %w", s.source, err)
		}
		if s.Item.Number == 0 {
			s.Item.Number = 1
		}
		ex := set.Explain(s.Item, env)
		var decides []string
		for _, d := range ex.Decisions() {
			decides = append(decides, d.Rule+" "+d.Outcome)
		}
		res := scenarioResult{Name: s.Name, Source: s.source, Decides: decides, Expect: s.Expect, Explanation: ex}
		if s.Expect != nil {
			pass := strings.Join(*s.Expect, ",") == strings.Join(decides, ",")
			res.Pass = &pass
			if !pass {
				failed++
			}
		}
		results = append(results, res)
		if output == "json" {
			continue
		}
		if i > 0 {
			fmt.Fprintln(out)
		}
		title := s.Name
		if title == "" {
			title = s.source
		}
		fmt.Fprintf(out, "%s  (%s, %s #%d, at %s)\n", title, s.source, s.Item.Kind, s.Item.Number, env.Now.UTC().Format(time.RFC3339))
		fmt.Fprintf(out, "facts   %s\n", factsLine(s.Item))
		if ex.Gate != "" {
			fmt.Fprintf(out, "gate    the item %s: no rule decides\n", ex.Gate)
		}
		fmt.Fprintln(out)
		printSteps(out, set, ex)
		if res.Pass != nil {
			want := *s.Expect
			if len(want) == 0 {
				want = []string{"nothing"}
			}
			if *res.Pass {
				fmt.Fprintf(out, "expect  %s  ✓\n", strings.Join(want, ", "))
			} else {
				fmt.Fprintf(out, "expect  %s  ✗ FAIL\n", strings.Join(want, ", "))
			}
		}
	}
	if output == "json" {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(results); err != nil {
			return err
		}
	} else if len(scenarios) > 1 {
		fmt.Fprintf(out, "\n%d scenarios, %d failed\n", len(scenarios), failed)
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d scenarios decided otherwise than expected", failed, len(scenarios))
	}
	return nil
}

// env is the scenario's overseer side, its unset fields the defaults'.
func (s scenario) env(defaults scenarioEnv) (rules.Env, error) {
	e := s.Env
	if e.Trigger == "" {
		e.Trigger = defaults.Trigger
	}
	if e.Watcher == "" {
		e.Watcher = defaults.Watcher
	}
	if len(e.Pool) == 0 {
		e.Pool = defaults.Pool
	}
	if e.Now.IsZero() {
		e.Now = time.Now().UTC()
	}
	var inactivity time.Duration
	if e.Inactivity != "" {
		d, err := time.ParseDuration(e.Inactivity)
		if err != nil {
			return rules.Env{}, fmt.Errorf("env.inactivity %q: %w", e.Inactivity, err)
		}
		inactivity = d
	}
	return rules.Env{
		Trigger: e.Trigger, Prefixes: conventions.Prefixes(e.Trigger), Watcher: e.Watcher,
		Pool: e.Pool, PRLabels: e.PRLabels, PRInactivityTimeout: inactivity, Now: e.Now,
	}, nil
}
