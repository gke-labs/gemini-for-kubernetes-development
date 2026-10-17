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
	want := []string{"adopt-pr", "chore", "ci", "comments", "conflict", "fix", "inactive",
		"inherit-labels", "nudge", "ready", "ready-unreview", "review", "stop", "workflow"}
	var got []string
	for _, r := range set {
		got = append(got, r.Name)
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("rules = %v, want %v", got, want)
	}
	// Each file says what code it mirrors.
	entries, _ := builtinFS.ReadDir(".")
	for _, e := range entries {
		data, _ := builtinFS.ReadFile(e.Name())
		if !strings.HasPrefix(string(data), "# Mirrors ") {
			t.Errorf("%s does not start with a # Mirrors comment", e.Name())
		}
	}
}

func TestBuiltinPRCareGroupOrder(t *testing.T) {
	set, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range set.Groups()["pr-care"] {
		got = append(got, r.Name)
	}
	if want := "comments conflict ci review ready ready-unreview"; strings.Join(got, " ") != want {
		t.Errorf("pr-care = %v, want %s", got, want)
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
as: author
`

func TestValidateErrors(t *testing.T) {
	for _, tc := range []struct {
		name, yaml, want string
	}{
		{"unknown field", base + "colour: red\n", "field colour not found"},
		{"unknown predicate field", strings.Replace(base, "labels:", "labelz:", 1), "field labelz not found"},
		{"valid", base, ""},
		{"issue predicate on pr", strings.Replace(base, "when:\n", "when:\n  linked-pr: none\n", 1), "when.linked-pr is not a predicate of a rule on pr"},
		{"pr predicate on issue rule", strings.Replace(strings.Replace(base, "on: pr", "on: issue", 1), "when:\n", "when:\n  checks: failing\n", 1), "when.checks is not a predicate of a rule on issue"},
		{"bad on", strings.Replace(base, "on: pr", "on: tuesday", 1), `on: "tuesday"`},
		{"bad enum", strings.Replace(base, "when:\n", "when:\n  checks: red\n", 1), `checks: "red" is not one of`},
		{"unknown recipe", strings.Replace(base, "care-ci", "care-nope", 1), `run.recipe "care-nope" is not one of factory's recipes`},
		{"recipe as watcher", strings.Replace(base, "as: author", "as: watcher", 1), "a recipe runs as"},
		{"recipe without as", strings.Replace(base, "as: author\n", "", 1), "a recipe runs as"},
		{"writes as coder", "name: r\non: pr\nwhen: {labels: {any: [x]}}\nrun: {label: [y]}\nas: coder\n", "GitHub writes are the watcher's"},
		{"recipe and writes", base[:strings.Index(base, "as:")] + "  label: [x]\nas: author\n", "not both"},
		{"no action", "name: r\non: pr\nwhen: {labels: {any: [x]}}\nrun: {}\n", "run needs a recipe or a GitHub write"},
		{"no narrowing", strings.Replace(base, "labels: {any: [overseer]}", "checks: failing", 1), "a predicate a listing can narrow on"},
		{"labels none does not narrow", strings.Replace(base, "any: [overseer]", "none: [overseer]", 1), "a predicate a listing can narrow on"},
		{"assignee none does not narrow", strings.Replace(base, "labels: {any: [overseer]}", "assignee: none", 1), "a predicate a listing can narrow on"},
		{"give-up over the floor", base + "policy:\n  give-up: {attempts: 11}\n", "give-up.attempts 11: want 1 to 10"},
		{"attempts over the floor", strings.Replace(base, "when:\n", "when:\n  attempts: {max: 50}\n", 1), "when.attempts.max 50: want 1 to 10"},
		{"cooldown under the floor", base + "policy:\n  cooldown: {failure: 30s}\n", "policy.cooldown 30s is under the floor of 1m"},
		{"bad duration", base + "policy:\n  cooldown: {success: soon}\n", `"soon" is not a duration`},
		{"head on issue", strings.Replace(base, "on: pr", "on: issue", 1) + "expect: [head]\n", `expect "head"`},
		{"group order and last", base + "group: {name: g, order: 1, last: true}\n", "give an order above 0 or last"},
		{"group neither", base + "group: {name: g}\n", "give an order above 0 or last"},
		{"event without event", "name: r\non: event\nwhen: {}\nrun: {recipe: workflow}\nas: agent\n", "needs when.event"},

		// from: the listings, which narrow by themselves.
		{"from narrows", strings.Replace(base, "when:\n  labels: {any: [overseer]}\n", "from: [labelled, assigned]\nwhen:\n  checks: failing\n", 1), ""},
		{"from of issues on pr", strings.Replace(base, "when:", "from: [opened-by-watcher]\nwhen:", 1), `from: "opened-by-watcher" is not a listing of rules on pr`},

		// any-of: one level, two sets or more, each narrowing unless from does.
		{"any-of", strings.Replace(base, "labels: {any: [overseer]}", "any-of:\n    - labels: {any: [a]}\n    - assignee: pool", 1), ""},
		{"any-of of one", strings.Replace(base, "labels: {any: [overseer]}", "any-of:\n    - labels: {any: [a]}", 1), "give two sets or more"},
		{"any-of nested", strings.Replace(base, "when:\n", "when:\n  any-of:\n    - {checks: failing}\n    - any-of: [{checks: pending}, {checks: green}]\n", 1), "has no any-of of its own"},
		{"any-of predicate of another kind", strings.Replace(base, "when:\n", "when:\n  any-of: [{checks: failing}, {linked-pr: none}]\n", 1), "when.any-of[1].linked-pr is not a predicate of a rule on pr"},
		{"any-of does not narrow", strings.Replace(base, "labels: {any: [overseer]}", "any-of: [{labels: {any: [a]}}, {checks: failing}]", 1), "give from, or a predicate"},
		{"closes without labels", strings.Replace(base, "when:\n", "when:\n  closes: {}\n", 1), "closes needs labels"},

		// The counts' resets, the cooldown's reset and skip.
		{"since a list", strings.Replace(base, "when:\n", "when:\n  attempts: {since: [head, human, pause], max: 3}\n", 1), ""},
		{"since unknown", strings.Replace(base, "when:\n", "when:\n  attempts: {since: [head, moon], max: 3}\n", 1), `since: "moon"`},
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
	member := func(name, group string) *Rule {
		return mustParse(t, strings.Replace(base, "name: r", "name: "+name, 1)+group)
	}
	for _, tc := range []struct {
		name string
		set  Set
		want string
	}{
		{"ok", Set{member("a", "group: {name: g, order: 1}\n"), member("b", "group: {name: g, order: 2}\n"), member("c", "group: {name: g, last: true}\n")}, ""},
		{"duplicate name", Set{member("a", ""), member("a", "")}, "two rules are named a"},
		{"duplicate order", Set{member("a", "group: {name: g, order: 1}\n"), member("b", "group: {name: g, order: 1}\n")}, "a and b share order 1"},
		{"two last", Set{member("a", "group: {name: g, last: true}\n"), member("b", "group: {name: g, last: true}\n")}, "a and b are both last"},
		{"mixed kinds", Set{member("a", "group: {name: g, order: 1}\n"), mustParse(t, "name: b\non: issue\ngroup: {name: g, order: 2}\nwhen: {labels: {any: [x]}}\nrun: {label: [y]}\n")}, "holds rules on pr and on issue"},
		{"invalid member", Set{member("a", "group: {name: g}\n")}, "rule a: group g: give an order above 0 or last"},
		{"before a group", Set{member("a", "group: {name: g, order: 1}\n"), member("b", "policy: {before: g}\n")}, ""},
		{"before no group", Set{member("b", "policy: {before: g}\n")}, "there is no group g"},
		{"before from a group", Set{member("a", "group: {name: g, order: 1}\n"), member("b", "group: {name: h, order: 1}\npolicy: {before: g}\n")}, "a rule in a group is ordered by it"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.set.Validate()
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

func TestGroupsOrder(t *testing.T) {
	r := func(name, group string) *Rule {
		return mustParse(t, strings.Replace(base, "name: r", "name: "+name, 1)+group)
	}
	set := Set{r("last", "group: {name: g, last: true}\n"), r("thirty", "group: {name: g, order: 30}\n"), r("ten", "group: {name: g, order: 10}\n")}
	var got []string
	for _, x := range set.Groups()["g"] {
		got = append(got, x.Name)
	}
	if want := "ten thirty last"; strings.Join(got, " ") != want {
		t.Errorf("order = %v, want %s", got, want)
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
		"ci.yaml": {Data: []byte("name: ci\non: pr\ngroup: {name: pr-care, order: 30}\n" +
			"when: {labels: {any: [\"{{trigger}}\"]}, checks: failing}\nrun: {recipe: care-ci}\nas: author\n" +
			"policy: {give-up: {attempts: 5}}\n")},
		"docs.yaml": {Data: []byte("name: docs\non: issue\nwhen: {labels: {any: [docs]}}\nrun: {recipe: fix}\nas: coder\n")},
		"README.md": {Data: []byte("not a rule")},
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
		"a replacement under the floor": {"ci.yaml": {Data: []byte("name: ci\non: pr\ngroup: {name: pr-care, order: 30}\n" +
			"when: {labels: {any: [x]}}\nrun: {recipe: care-ci}\nas: author\npolicy: {give-up: {attempts: 50}}\n")}},
		"a name only, no enabled": {"review.yaml": {Data: []byte("name: review\n")}},
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
