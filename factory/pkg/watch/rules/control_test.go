package rules

import (
	"strings"
	"testing"
)

// The engine decides every rule but a disabled one, which nothing
// decides; an unknown rule cannot be disabled; a nil Control is the
// scanners alone.
func TestNewControl(t *testing.T) {
	set, _ := Builtin()
	if _, err := NewControl([]string{"nope"}, set); err == nil || !strings.Contains(err.Error(), "there is no rule nope") {
		t.Errorf("an unknown rule disabled: %v", err)
	}
	c, err := NewControl([]string{"review"}, set)
	if err != nil {
		t.Fatal(err)
	}
	switch {
	case !c.Takes("conflict") || !c.Takes("ci"):
		t.Error("the engine does not take a rule")
	case c.Takes("review"):
		t.Error("the engine takes review, disabled")
	case !c.TakesAny("review", "ci") || c.TakesAny("review"):
		t.Error("TakesAny is wrong")
	case !c.Off("review") || c.Off("ci"):
		t.Error("Off is wrong")
	}
	for _, r := range c.Set() {
		if r.Name == "review" && r.IsEnabled() {
			t.Error("the engine's review is on, though disabled")
		}
	}
	if s := c.String(); !strings.Contains(s, "the engine decides") || !strings.Contains(s, "disabled review") {
		t.Errorf("String = %q", s)
	}
	var none *Control
	if none.Off("ci") || none.Takes("ci") || none.Decide(Item{}, testEnv) != nil {
		t.Error("a nil control is not the scanners alone")
	}
}

func TestParseList(t *testing.T) {
	if got := strings.Join(ParseList("review, ci,,"), "|"); got != "review|ci" {
		t.Errorf("list = %q", got)
	}
}
