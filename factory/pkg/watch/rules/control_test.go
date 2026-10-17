package rules

import (
	"strings"
	"testing"
)

func TestNewControl(t *testing.T) {
	set, _ := Builtin()
	if c, err := NewControl("", nil, "", nil, set); c != nil || err != nil {
		t.Errorf("defaults = %v, %v; want nil", c, err)
	}
	if c, err := NewControl("off", map[string]string{}, "", nil, set); c != nil || err != nil {
		t.Errorf("off = %v, %v; want nil", c, err)
	}
	for _, tc := range []struct {
		engine   string
		use      map[string]string
		disabled []string
		want     string
	}{
		{"maybe", nil, nil, `rules engine "maybe"`},
		{"on", map[string]string{"nope": "engine"}, nil, "there is no rule nope"},
		{"on", map[string]string{"ci": "sometimes"}, nil, `ci="sometimes"`},
		{"", nil, []string{"nope"}, "there is no rule nope"},
	} {
		if _, err := NewControl(tc.engine, tc.use, "", tc.disabled, set); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: err = %v, want %q", tc, err, tc.want)
		}
	}

	c, err := NewControl("on", map[string]string{"conflict": "engine", "ci": "legacy", "review": "engine"}, "overseer/engine", []string{"review"}, set)
	if err != nil {
		t.Fatal(err)
	}
	canary := []string{"overseer", "overseer/engine"}
	switch {
	case !c.Takes("conflict", canary):
		t.Error("the engine does not take conflict on a canary item")
	case c.Takes("conflict", []string{"overseer"}):
		t.Error("the engine takes conflict on an item without the canary label")
	case c.Takes("ci", canary):
		t.Error("the engine takes ci, left legacy")
	case c.Takes("review", canary):
		t.Error("the engine takes review, disabled")
	case !c.Off("review") || c.Off("ci"):
		t.Error("Off is wrong")
	case !c.Shadows():
		t.Error("on does not shadow")
	}
	for _, r := range c.Set() {
		if r.Name == "review" && r.IsEnabled() {
			t.Error("the engine's review is on, though disabled")
		}
	}
	if s := c.String(); !strings.Contains(s, "engine decides conflict on items labelled overseer/engine") || !strings.Contains(s, "disabled review") {
		t.Errorf("String = %q", s)
	}
	var none *Control
	if none.Off("ci") || none.Takes("ci", nil) || none.Shadows() || none.Decide(Item{}, testEnv) != nil {
		t.Error("a nil control is not the scanners alone")
	}
}

func TestParseUseAndList(t *testing.T) {
	use, err := ParseUse(" conflict=engine, ci = legacy ,")
	if err != nil || use["conflict"] != "engine" || use["ci"] != "legacy" || len(use) != 2 {
		t.Errorf("use = %v, %v", use, err)
	}
	if _, err := ParseUse("conflict"); err == nil {
		t.Error("a use without =")
	}
	if got := strings.Join(ParseList("review, ci,,"), "|"); got != "review|ci" {
		t.Errorf("list = %q", got)
	}
}
