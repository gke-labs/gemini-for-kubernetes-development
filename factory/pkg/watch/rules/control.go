package rules

import (
	"fmt"
	"slices"
	"strings"
)

// Control is the operator's say over the rules in a watch (the Overseer's
// spec.rules): the rules the engine decides with, and which of them the
// watch does not act on at all (Disabled, the emergency switch). The
// engine decides every rule; a nil Control (a watch built without one, as
// some tests are) leaves the scanners to decide alone.
type Control struct {
	Disabled []string

	// set is the rules the engine decides with: the prebaked ones, those
	// Disabled turned off.
	set Set
}

// NewControl checks disabled against set and returns the Control.
func NewControl(disabled []string, set Set) (*Control, error) {
	names := map[string]bool{}
	for _, r := range set {
		names[r.Name] = true
	}
	for _, rule := range disabled {
		if !names[rule] {
			return nil, fmt.Errorf("rules disabled: there is no rule %s", rule)
		}
	}
	off := false
	var toggles []*Rule
	for _, rule := range disabled {
		toggles = append(toggles, &Rule{Name: rule, Enabled: &off})
	}
	overlaid, err := Overlay(set, toggles)
	if err != nil {
		return nil, err
	}
	return &Control{Disabled: disabled, set: overlaid}, nil
}

// ParseList reads "a,b,c".
func ParseList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// Set is the rules the engine decides with, Disabled turned off.
func (c *Control) Set() Set {
	if c == nil {
		return nil
	}
	return c.set
}

// Off is whether the watch must not act on rule at all: the emergency
// switch.
func (c *Control) Off(rule string) bool {
	return c != nil && slices.Contains(c.Disabled, rule)
}

// Takes is whether the engine decides rule for the scanner: always, but a
// disabled rule, which nothing decides.
func (c *Control) Takes(rule string) bool {
	return c != nil && !c.Off(rule)
}

// TakesAny is whether the engine decides any of rules.
func (c *Control) TakesAny(rules ...string) bool {
	return slices.ContainsFunc(rules, c.Takes)
}

// Decide is the engine's decisions for an item.
func (c *Control) Decide(it Item, env Env) []Decision {
	if c == nil {
		return nil
	}
	return c.set.Decide(it, env)
}

// String is the settings in a line, for the watch's log.
func (c *Control) String() string {
	if c == nil {
		return "rules: off"
	}
	s := fmt.Sprintf("rules: the engine decides %d rules", len(c.set))
	if len(c.Disabled) > 0 {
		s += ", disabled " + strings.Join(c.Disabled, ",")
	}
	return s
}

// Named is the set's rule of that name, nil if it has none.
func (s Set) Named(name string) *Rule {
	for _, r := range s {
		if r.Name == name {
			return r
		}
	}
	return nil
}
