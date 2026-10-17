package rules

import (
	"strings"
	"testing"
	"time"
)

func TestBuiltinRulesParseAndValidate(t *testing.T) {
	set, err := Builtin()
	if err != nil {
		t.Fatalf("Builtin: %v", err)
	}
	want := []string{"adopt-issue", "adopt-pr", "chore", "ci", "comments", "conflict", "fix", "inactive",
		"inherit-labels", "nudge", "ready", "review", "stop", "workflow"}
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
	if want := "comments conflict ci review ready"; strings.Join(got, " ") != want {
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
	// Off as today: no inactivity timeout by default; stop is a global gate.
	for _, name := range []string{"inactive", "stop"} {
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
