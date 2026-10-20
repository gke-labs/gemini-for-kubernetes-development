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

// The canary percent picks the same items every time, about that share of
// them, and needs a label to put on.
func TestControlCanary(t *testing.T) {
	set, _ := Builtin()
	c, err := NewControl("on", map[string]string{"ci": UseEngine}, "overseer/rules-canary", nil, set)
	if err != nil {
		t.Fatal(err)
	}
	if c.Canary(7) {
		t.Error("0%: an item picked")
	}
	if err := c.SetCanaryPercent(20); err != nil {
		t.Fatal(err)
	}
	// Another watch, or the next one after a restart, picks the same.
	again, _ := NewControl("on", map[string]string{"ci": UseEngine}, "overseer/rules-canary", nil, set)
	if err := again.SetCanaryPercent(20); err != nil {
		t.Fatal(err)
	}
	picked := 0
	for n := 1; n <= 10000; n++ {
		got := c.Canary(n)
		if got {
			picked++
		}
		if again.Canary(n) != got {
			t.Fatalf("#%d picked by one watch and not the other", n)
		}
	}
	if picked < 1700 || picked > 2300 {
		t.Errorf("20%% picked %d of 10000", picked)
	}
	if err := c.SetCanaryPercent(100); err != nil || !c.Canary(12345) {
		t.Errorf("100%%: %v, picked %v", err, c.Canary(12345))
	}
	if !strings.Contains(c.String(), "the watch labels 100% of them") {
		t.Errorf("String = %q", c.String())
	}
	for _, p := range []int{-1, 101} {
		if err := c.SetCanaryPercent(p); err == nil {
			t.Errorf("percent %d accepted", p)
		}
	}
	nolabel, _ := NewControl("on", map[string]string{"ci": UseEngine}, "", nil, set)
	if err := nolabel.SetCanaryPercent(20); err == nil || !strings.Contains(err.Error(), "needs a canary label") {
		t.Errorf("no label: %v", err)
	}
	var none *Control
	if err := none.SetCanaryPercent(20); err == nil {
		t.Error("nil control took a percent")
	}
}
