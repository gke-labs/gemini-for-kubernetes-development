package rules

import (
	"fmt"
	"hash/fnv"
	"slices"
	"sort"
	"strings"
)

// How the rules run beside the watch's scanners (Control.Engine).
const (
	// EngineOff is the scanners alone, as before the rules.
	EngineOff = "off"
	// EngineShadow decides beside the scanners and acts on nothing.
	EngineShadow = "shadow"
	// EngineOn is shadow, plus the rules handed to the engine (Use)
	// deciding for the scanners.
	EngineOn = "on"
)

// What decides a rule (Control.Use).
const (
	UseLegacy = "legacy"
	UseEngine = "engine"
)

// Control is the operator's say over the rules in a watch (the Overseer's
// spec.rules): whether they run (Engine), which rules the engine decides
// for the scanners (Use, on items with CanaryLabel when one is set), and
// which rules the watch does not act on at all (Disabled, the emergency
// switch, through the scanners or the engine). A nil Control is the
// scanners alone, every rule on: today's watch.
type Control struct {
	Engine      string
	Use         map[string]string
	CanaryLabel string
	// CanaryPercent is the share of items the watch puts CanaryLabel on
	// itself (Canary); 0 labels none.
	CanaryPercent int
	Disabled      []string

	// set is the rules the engine decides with: the prebaked ones, those
	// Disabled turned off.
	set Set
}

// NewControl checks the settings against set and returns the Control, or
// nil when every setting is its default (engine off, nothing used or
// disabled).
func NewControl(engine string, use map[string]string, canaryLabel string, disabled []string, set Set) (*Control, error) {
	switch engine {
	case "", EngineOff:
		engine = EngineOff
	case EngineShadow, EngineOn:
	default:
		return nil, fmt.Errorf("rules engine %q: want off, shadow or on", engine)
	}
	names := map[string]bool{}
	for _, r := range set {
		names[r.Name] = true
	}
	for rule, by := range use {
		if !names[rule] {
			return nil, fmt.Errorf("rules use: there is no rule %s", rule)
		}
		if by != UseLegacy && by != UseEngine {
			return nil, fmt.Errorf("rules use: %s=%q: want legacy or engine", rule, by)
		}
	}
	for _, rule := range disabled {
		if !names[rule] {
			return nil, fmt.Errorf("rules disabled: there is no rule %s", rule)
		}
	}
	if engine == EngineOff && len(use) == 0 && canaryLabel == "" && len(disabled) == 0 {
		return nil, nil
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
	return &Control{Engine: engine, Use: use, CanaryLabel: canaryLabel, Disabled: disabled, set: overlaid}, nil
}

// SetCanaryPercent sets the share of items the watch labels with the
// canary label: 0 to 100, and only with a canary label to put on.
func (c *Control) SetCanaryPercent(percent int) error {
	switch {
	case percent == 0:
		return nil
	case percent < 0 || percent > 100:
		return fmt.Errorf("rules canary percent %d: want 0 to 100", percent)
	case c == nil || c.CanaryLabel == "":
		return fmt.Errorf("rules canary percent %d: needs a canary label to put on", percent)
	}
	c.CanaryPercent = percent
	return nil
}

// Canary is whether the item numbered number is one the watch puts the
// canary label on: its number's hash falls in CanaryPercent, so the same
// items every pass, and new ones at the same rate.
func (c *Control) Canary(number int) bool {
	if c == nil || c.CanaryPercent <= 0 || c.CanaryLabel == "" || number <= 0 {
		return false
	}
	h := fnv.New32a()
	fmt.Fprintf(h, "%d", number)
	return int(h.Sum32()%100) < c.CanaryPercent
}

// ParseUse reads "rule=engine,rule=legacy".
func ParseUse(s string) (map[string]string, error) {
	out := map[string]string{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		rule, by, ok := strings.Cut(part, "=")
		if !ok {
			return nil, fmt.Errorf("rules use %q: want rule=engine or rule=legacy", part)
		}
		out[strings.TrimSpace(rule)] = strings.TrimSpace(by)
	}
	return out, nil
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

// Shadows is whether the rules decide beside the scanners (shadow or on).
func (c *Control) Shadows() bool {
	return c != nil && (c.Engine == EngineShadow || c.Engine == EngineOn)
}

// Off is whether the watch must not act on rule at all: the emergency
// switch.
func (c *Control) Off(rule string) bool {
	return c != nil && slices.Contains(c.Disabled, rule)
}

// Takes is whether the engine decides rule for the scanner on an item with
// labels: the engine on, the rule used by the engine, and the item carrying
// the canary label when there is one.
func (c *Control) Takes(rule string, labels []string) bool {
	if c == nil || c.Engine != EngineOn || c.Use[rule] != UseEngine || c.Off(rule) {
		return false
	}
	if c.CanaryLabel == "" {
		return true
	}
	return slices.ContainsFunc(labels, func(l string) bool { return strings.EqualFold(l, c.CanaryLabel) })
}

// TakesAny is whether the engine decides any of rules on the item.
func (c *Control) TakesAny(labels []string, rules ...string) bool {
	return slices.ContainsFunc(rules, func(r string) bool { return c.Takes(r, labels) })
}

// Decide is the engine's decisions for an item.
func (c *Control) Decide(it Item, env Env) []Decision {
	if c == nil {
		return nil
	}
	return c.set.Decide(it, env)
}

// Decided is whether ds has a decision of rule (any outcome).
func Decided(ds []Decision, rule string) bool {
	return slices.ContainsFunc(ds, func(d Decision) bool { return d.Rule == rule })
}

// String is the settings in a line, for the watch's log.
func (c *Control) String() string {
	if c == nil {
		return "rules: off"
	}
	var used []string
	for rule, by := range c.Use {
		if by == UseEngine && !c.Off(rule) {
			used = append(used, rule)
		}
	}
	sort.Strings(used)
	s := fmt.Sprintf("rules: engine %s", c.Engine)
	if len(used) > 0 {
		s += ", engine decides " + strings.Join(used, ",")
		if c.CanaryLabel != "" {
			s += " on items labelled " + c.CanaryLabel
		}
		if c.CanaryPercent > 0 {
			s += fmt.Sprintf(" (the watch labels %d%% of them)", c.CanaryPercent)
		}
	}
	if len(c.Disabled) > 0 {
		s += ", disabled " + strings.Join(c.Disabled, ",")
	}
	return s
}
