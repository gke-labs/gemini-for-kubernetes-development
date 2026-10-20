package rules

import (
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func TestBuiltinRulesParseAndValidate(t *testing.T) {
	set, err := Builtin()
	if err != nil {
		t.Fatalf("Builtin: %v", err)
	}
	// The kinds' files, each top to bottom.
	want := []string{"workflow", "fix", "stop",
		"inherit-labels", "inactive", "comments", "conflict", "ci", "review", "ready", "ready-unreview", "adopt-pr",
		"chore", "nudge"}
	var got []string
	for _, r := range set {
		got = append(got, r.Name)
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("rules = %v, want %v", got, want)
	}
	// Each rule says what code it mirrors, in a comment above it.
	for _, kind := range Kinds {
		data, _ := builtinFS.ReadFile(kind + ".yaml")
		rules := 0
		for _, r := range set {
			if r.On == kind {
				rules++
			}
		}
		if n := strings.Count(string(data), "\n  # Mirrors "); n != rules {
			t.Errorf("%s.yaml: %d # Mirrors comments for %d rules", kind, n, rules)
		}
	}
}

// The PR rules in order, those that take the item marked: the label
// sync and the pause first, then today's pr-care order.
func TestBuiltinPROrder(t *testing.T) {
	set, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range set.Ordered() {
		if r.On != OnPR {
			continue
		}
		name := r.Name
		if r.IsExclusive() {
			name += "*"
		}
		got = append(got, name)
	}
	if want := "inherit-labels inactive comments* conflict* ci* review* ready* ready-unreview* adopt-pr"; strings.Join(got, " ") != want {
		t.Errorf("PR rules = %v, want %s", got, want)
	}
}

func TestBuiltinDefaults(t *testing.T) {
	set, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*Rule{}
	for _, r := range set {
		byName[r.Name] = r
	}
	fix := byName["fix"]
	if !fix.IsEnabled() || fix.Policy.Cooldown.Success.Duration != 10*time.Minute || fix.Policy.Cooldown.Failure.Duration != 20*time.Minute {
		t.Errorf("fix = %+v", fix.Policy.Cooldown)
	}
	// Off: stop is a global gate; adopt-pr, a safety net open-pr made idle.
	// inactive is on, and a no-op without a timeout.
	if !byName["inactive"].IsEnabled() {
		t.Error("inactive is off")
	}
	for _, name := range []string{"stop", "adopt-pr"} {
		if byName[name].IsEnabled() {
			t.Errorf("%s is enabled", name)
		}
	}
}

const base = `
name: r
on: pr
when:
  labels: {any: [overseer]}
run:
  recipe: care-ci
  apply: post-replies
`

func TestValidateErrors(t *testing.T) {
	for _, tc := range []struct {
		name, yaml, want string
	}{
		{"unknown field", base + "colour: red\n", "field colour not found"},
		{"as", base + "as: author\n", "field as not found"},
		{"unknown predicate field", strings.Replace(base, "labels:", "labelz:", 1), "field labelz not found"},
		{"valid", base, ""},
		{"issue predicate on pr", strings.Replace(base, "when:\n", "when:\n  linked-pr: none\n", 1), "when.linked-pr is not a predicate of a rule on pr"},
		{"pr predicate on issue rule", strings.Replace(strings.Replace(base, "on: pr", "on: issue", 1), "when:\n", "when:\n  checks: failing\n", 1), "when.checks is not a predicate of a rule on issue"},
		{"bad on", strings.Replace(base, "on: pr", "on: tuesday", 1), `on: "tuesday"`},
		{"bad enum", strings.Replace(base, "when:\n", "when:\n  checks: red\n", 1), `checks: "red" is not one of`},
		{"unknown recipe", strings.Replace(base, "care-ci", "care-nope", 1), `run.recipe "care-nope" is not one of factory's recipes`},
		{"recipe and writes", base + "  label: [x]\n", "not both"},
		{"no action", "name: r\non: pr\nwhen: {labels: {any: [x]}}\nrun: {}\n", "run needs a recipe or a GitHub write"},
		{"give-up over the floor", base + "policy:\n  give-up: {attempts: 11}\n", "give-up.attempts 11: want 1 to 10"},
		{"attempts in when", strings.Replace(base, "when:\n", "when:\n  attempts: {max: 3}\n", 1), "field attempts not found"},
		{"updated-since-run in when", strings.Replace(base, "when:\n", "when:\n  updated-since-run: true\n", 1), "field updated-since-run not found"},
		{"stopped", strings.Replace(base, "when:\n", "when:\n  stopped: false\n", 1), "field stopped not found"},
		{"merge-queue", strings.Replace(base, "when:\n", "when:\n  merge-queue: false\n", 1), "field merge-queue not found"},
		{"author trusted", strings.Replace(base, "when:\n", "when:\n  author: trusted\n", 1), `author: "trusted" is not one of`},
		{"assignee owner", strings.Replace(base, "when:\n", "when:\n  assignee: owner\n", 1), `assignee: "owner" is not one of`},
		{"give-up then", base + "policy:\n  give-up: {attempts: 3, then: stop}\n", "field then not found"},
		{"dedupe update", base + "policy:\n  dedupe: update\n", ""},
		{"dedupe item", base + "policy:\n  dedupe: item\n", `dedupe: "item" is not one of`},
		{"identity-pin", base + "policy:\n  identity-pin: false\n", "field identity-pin not found"},
		{"skip-cooldown", base + "policy:\n  skip-cooldown: true\n", "field skip-cooldown not found"},
		{"cooldown under the floor", base + "policy:\n  cooldown: {failure: 30s}\n", "policy.cooldown 30s is under the floor of 1m"},
		{"bad duration", base + "policy:\n  cooldown: {success: soon}\n", `"soon" is not a duration`},
		{"expect", base + "expect: [head]\n", "field expect not found"},
		{"group", base + "group: {name: g, order: 1}\n", "field group not found"},
		{"writes without order", "name: r\non: pr\nwhen: {labels: {any: [x]}}\nrun: {label: [y]}\n", ""},
		{"order", base + "order: 30\n", "field order not found"},
		{"exclusive on event", "name: r\non: event\nexclusive: true\nwhen: {event: linked-issue-closed}\nrun: {recipe: workflow}\n", "exclusive is for rules on issue or pr"},
		{"before and after", base + "before: ci\nafter: review\n", "before or after, not both"},
		{"before itself", base + "before: r\n", "before or after itself"},
		{"event without event", "name: r\non: event\nwhen: {}\nrun: {recipe: workflow}\n", "needs when.event"},

		// from: gone; on and when say which items a rule takes.
		{"from", strings.Replace(base, "when:", "from: [labelled, assigned]\nwhen:", 1), "field from not found"},

		// any-of: one level, two sets or more.
		{"any-of", strings.Replace(base, "labels: {any: [overseer]}", "any-of:\n    - labels: {any: [a]}\n    - assignee: pool", 1), ""},
		{"any-of of one", strings.Replace(base, "labels: {any: [overseer]}", "any-of:\n    - labels: {any: [a]}", 1), "give two sets or more"},
		{"any-of nested", strings.Replace(base, "when:\n", "when:\n  any-of:\n    - {checks: failing}\n    - any-of: [{checks: pending}, {checks: green}]\n", 1), "has no any-of of its own"},
		{"any-of predicate of another kind", strings.Replace(base, "when:\n", "when:\n  any-of: [{checks: failing}, {linked-pr: none}]\n", 1), "when.any-of[1].linked-pr is not a predicate of a rule on pr"},
		{"closes without labels", strings.Replace(base, "when:\n", "when:\n  closes: {}\n", 1), "closes needs labels"},

		// The counts' resets, the cooldown's reset and skip.
		{"since a list", base + "policy:\n  give-up: {attempts: 3, since: [head, human, pause]}\n", ""},
		{"since unknown", base + "policy:\n  give-up: {attempts: 3, since: [head, moon]}\n", `since: "moon"`},
		{"reset-on head on issue", strings.Replace(base, "on: pr", "on: issue", 1) + "policy:\n  cooldown: {success: 1h, reset-on: [head]}\n", "reset-on: head is a PR's"},
		{"skip-when beyond assignee", base + "policy:\n  cooldown: {success: 1h, skip-when: {checks: failing}}\n", "skip-when says assignee, and only that"},

		// Placeholders and named durations are the engine's closed set.
		{"unknown placeholder", strings.Replace(base, "[overseer]", `["{{nope}}"]`, 1), "{{nope}} is not one of {{trigger}}"},
		{"pr placeholder on issue", "name: r\non: issue\nwhen: {labels: {any: [x]}}\nrun: {label: [\"{{closing-issue-labels}}\"]}\n", "{{closing-issue-labels}} is not one of"},
		{"unknown duration name", base + "policy:\n  cooldown: {success: \"{{soon}}\"}\n", "is not a duration factory reads"},
		{"named duration", base + "policy:\n  cooldown: {success: \"{{workflow-cooldown}}\"}\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := Parse([]byte(tc.yaml))
			if err == nil {
				err = r.Validate()
			}
			if tc.want == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func mustParse(t *testing.T, s string) *Rule {
	t.Helper()
	r, err := Parse([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSetValidate(t *testing.T) {
	member := func(name string) *Rule {
		return mustParse(t, strings.Replace(base, "name: r", "name: "+name, 1))
	}
	if err := (Set{member("a"), member("b")}).Validate(); err != nil {
		t.Errorf("two rules: %v", err)
	}
	if err := (Set{member("a"), member("a")}).Validate(); err == nil || !strings.Contains(err.Error(), "two rules are named a") {
		t.Errorf("duplicate name: %v", err)
	}
}

// A kind's file is its rules in order: its on is its name, and a rule in
// it takes the file's on and its place from its position.
func TestParseKindFile(t *testing.T) {
	rs, err := parseKindFile([]byte("on: pr\nrules:\n  - name: a\n    when: {}\n    run: {label: [x]}\n  - name: b\n    when: {}\n    run: {label: [y]}\n"), OnPR)
	if err != nil || len(rs) != 2 || rs[0].Name != "a" || rs[1].On != OnPR {
		t.Fatalf("rules = %+v, %v", rs, err)
	}
	for name, tc := range map[string]struct{ data, want string }{
		"another kind":     {"on: issue\nrules: []\n", `want "pr"`},
		"a rule's own on":  {"on: pr\nrules:\n  - name: a\n    on: pr\n    when: {}\n    run: {label: [x]}\n", "on is the file's"},
		"a placed rule":    {"on: pr\nrules:\n  - name: a\n    before: b\n    when: {}\n    run: {label: [x]}\n", "placed by its position"},
		"an unknown field": {"on: pr\norder: 1\nrules: []\n", "field order not found"},
	} {
		if _, err := parseKindFile([]byte(tc.data), OnPR); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
}

func TestParseEmpty(t *testing.T) {
	if _, err := Parse(nil); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Errorf("err = %v", err)
	}
}

// A repository's rules lie over factory's: a toggle turns a factory rule
// on or off and keeps the rest of it, a whole rule replaces factory's or
// adds one, and each rule says whose it is.
func TestOverlay(t *testing.T) {
	base, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	repo, err := Load(fstest.MapFS{
		"review.yaml":   {Data: []byte("name: review\nenabled: false\n")},
		"inactive.yaml": {Data: []byte("name: inactive\nenabled: true\n")},
		"ci.yaml": {Data: []byte("name: ci\non: pr\n" +
			"when: {labels: {any: [\"{{trigger}}\"]}, checks: failing}\nrun: {recipe: care-ci}\n" +
			"policy: {give-up: {attempts: 5}}\n")},
		"docs.yaml":  {Data: []byte("name: docs\non: issue\nwhen: {labels: {any: [docs]}}\nrun: {recipe: fix}\n")},
		"lint.yaml":  {Data: []byte("name: lint\non: pr\nbefore: review\nwhen: {checks: green}\nrun: {recipe: review}\n")},
		"notes.yaml": {Data: []byte("name: notes\non: pr\nafter: lint\nwhen: {checks: green}\nrun: {label: [n]}\n")},
		"README.md":  {Data: []byte("not a rule")},
	})
	if err != nil {
		t.Fatal(err)
	}
	set, err := Overlay(base, repo)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*Rule{}
	for _, r := range set {
		byName[r.Name] = r
	}
	if r := byName["review"]; r.IsEnabled() || r.Source != SourceFactory || r.EnabledBy != SourceRepo || r.Run.Recipe != "review" {
		t.Errorf("review = enabled %v, source %s, enabledBy %s, run %+v; want factory's, off by the repository", r.IsEnabled(), r.Source, r.EnabledBy, r.Run)
	}
	if r := byName["inactive"]; !r.IsEnabled() || r.EnabledBy != SourceRepo {
		t.Errorf("inactive: enabled %v by %s; want on by the repository", r.IsEnabled(), r.EnabledBy)
	}
	if r := byName["ci"]; r.Source != SourceRepo || r.Policy.GiveUp.Attempts != 5 {
		t.Errorf("ci = %+v; want the repository's", r)
	}
	if r := byName["docs"]; r == nil || r.Source != SourceRepo {
		t.Errorf("docs = %+v; want the repository's new rule", r)
	}
	// The order: a replacement keeps its place, a new rule without an
	// anchor goes after its kind's rules, the others where they say, one
	// against another.
	var order []string
	for _, r := range set {
		order = append(order, r.Name)
	}
	if want := "workflow fix stop docs inherit-labels inactive comments conflict ci lint notes review ready ready-unreview adopt-pr chore nudge"; strings.Join(order, " ") != want {
		t.Errorf("order = %v\nwant    %s", order, want)
	}
	if r := byName["fix"]; r.Source != SourceFactory || r.EnabledBy != "" || !r.IsEnabled() {
		t.Errorf("fix = %+v; want factory's, untouched", r)
	}
	// base is not changed.
	for _, r := range base {
		if r.Name == "review" && (!r.IsEnabled() || r.EnabledBy != "") {
			t.Errorf("Overlay changed factory's review: %+v", r)
		}
	}

	for name, files := range map[string]fstest.MapFS{
		"toggle of a rule factory lacks": {"nope.yaml": {Data: []byte("name: nope\nenabled: false\n")}},
		"a replacement under the floor": {"ci.yaml": {Data: []byte("name: ci\non: pr\n" +
			"when: {labels: {any: [x]}}\nrun: {recipe: care-ci}\npolicy: {give-up: {attempts: 50}}\n")}},
		"a name only, no enabled":       {"review.yaml": {Data: []byte("name: review\n")}},
		"a replacement placed":          {"ci.yaml": {Data: []byte("name: ci\non: pr\nbefore: review\nwhen: {checks: failing}\nrun: {recipe: care-ci}\n")}},
		"a replacement on another kind": {"ci.yaml": {Data: []byte("name: ci\non: issue\nwhen: {}\nrun: {recipe: fix}\n")}},
		"an anchor that is not there":   {"x.yaml": {Data: []byte("name: x\non: pr\nbefore: nope\nwhen: {}\nrun: {label: [x]}\n")}},
		"an anchor on another kind":     {"x.yaml": {Data: []byte("name: x\non: pr\nbefore: fix\nwhen: {}\nrun: {label: [x]}\n")}},
		"a loop": {
			"x.yaml": {Data: []byte("name: x\non: pr\nafter: y\nwhen: {}\nrun: {label: [x]}\n")},
			"y.yaml": {Data: []byte("name: y\non: pr\nafter: x\nwhen: {}\nrun: {label: [y]}\n")},
		},
	} {
		repo, err := Load(files)
		if err == nil {
			_, err = Overlay(base, repo)
		}
		if err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if _, err := Load(fstest.MapFS{"a.yaml": {Data: []byte("name: b\nenabled: false\n")}}); err == nil {
		t.Error("a file not named after its rule loaded")
	}
}
