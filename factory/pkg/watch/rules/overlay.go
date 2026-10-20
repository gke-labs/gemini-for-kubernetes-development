package rules

import (
	"fmt"
	"io/fs"
	"reflect"
	"slices"
	"sort"
	"strings"
)

// Where a rule, or its enabled, came from.
const (
	// SourceFactory is factory's prebaked rules (Builtin).
	SourceFactory = "factory"
	// SourceRepo is a repository's rules, .agents/rules/<name>.yaml on its
	// default branch.
	SourceRepo = "repo"
)

// IsToggle reports whether the rule file only turns a rule of its name on
// or off: a name and enabled, and nothing else a rule says. A repository
// turns factory's rule off in code with one:
//
//	name: review
//	enabled: false
func (r *Rule) IsToggle() bool {
	return r.Enabled != nil && r.On == "" && r.Before == "" && r.After == "" && r.Exclusive == nil &&
		reflect.ValueOf(r.When).IsZero() && reflect.ValueOf(r.Run).IsZero() && reflect.ValueOf(r.Policy).IsZero()
}

// Load reads a directory of rule files, one per rule, each named after its
// rule (<name>.yaml), as a repository's .agents/rules/ holds them. The
// rules are not validated: Overlay does, once they are laid over the rules
// they replace or toggle.
func Load(fsys fs.FS) ([]*Rule, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	var out []*Rule
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".yaml")
		if !ok || e.IsDir() {
			continue
		}
		data, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			return nil, err
		}
		r, err := Parse(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		if r.Name != name {
			return nil, fmt.Errorf("%s: the rule is named %s; a rule's file is named after it", e.Name(), r.Name)
		}
		out = append(out, r)
	}
	return out, nil
}

// Overlay lays a repository's rules over base (factory's, in the order the
// engine walks them): a toggle turns the base rule of its name on or off,
// keeping the rest of it; a whole rule of a base rule's name replaces it in
// place, on the same kind; a rule of a new name is added before or after
// the rule on its kind it names (Before, After), or after the rules on its
// kind. New rules may be placed against each other; an anchor that is not
// a rule on the same kind, or a loop of them, is an error. Each rule's
// Source says whose it is, and EnabledBy whose enabled it carries when a
// toggle set it. base is not changed. The result is validated.
//
// Either way round: a repository may turn on a rule factory ships off
// (inactive), as well as off one it ships on. The overseer's operator has
// the last word, on the Overseer (spec.rules), over both.
func Overlay(base Set, repo []*Rule) (Set, error) {
	out := make(Set, 0, len(base)+len(repo))
	index := func(name string) int {
		return slices.IndexFunc(out, func(r *Rule) bool { return r.Name == name })
	}
	for _, r := range base {
		c := *r
		if c.Source == "" {
			c.Source = SourceFactory
		}
		out = append(out, &c)
	}
	var added []*Rule
	for _, r := range repo {
		c := *r
		i := index(c.Name)
		if c.IsToggle() {
			if i < 0 {
				return nil, fmt.Errorf("rule %s: turns on or off a rule factory does not have", c.Name)
			}
			out[i].Enabled = c.Enabled
			out[i].EnabledBy = SourceRepo
			continue
		}
		c.Source = SourceRepo
		if i >= 0 {
			switch {
			case c.Before != "" || c.After != "":
				return nil, fmt.Errorf("rule %s: replaces factory's in place, so it names no rule to go before or after", c.Name)
			case c.On != out[i].On:
				return nil, fmt.Errorf("rule %s: replaces factory's rule on %s with one on %s", c.Name, out[i].On, c.On)
			}
			out[i] = &c
			continue
		}
		added = append(added, &c)
	}
	sort.SliceStable(added, func(i, j int) bool { return added[i].Name < added[j].Name })
	// Each pass places the new rules whose anchor is placed; a pass that
	// places none leaves an anchor missing or a loop.
	for len(added) > 0 {
		var left []*Rule
		for _, c := range added {
			anchor := c.Before
			if anchor == "" {
				anchor = c.After
			}
			if anchor == "" {
				out = slices.Insert(out, lastOfKind(out, c.On)+1, c)
				continue
			}
			i := index(anchor)
			if i < 0 {
				left = append(left, c)
				continue
			}
			if out[i].On != c.On {
				return nil, fmt.Errorf("rule %s: %s is a rule on %s, not %s", c.Name, anchor, out[i].On, c.On)
			}
			if c.After != "" {
				i++
			}
			out = slices.Insert(out, i, c)
		}
		if len(left) == len(added) {
			var names []string
			for _, c := range left {
				names = append(names, c.Name)
			}
			return nil, fmt.Errorf("rules %s: placed before or after rules that are not there, or each other in a loop", strings.Join(names, ", "))
		}
		added = left
	}
	if err := out.Validate(); err != nil {
		return nil, err
	}
	return out, nil
}

// lastOfKind is the index of the last rule on kind in s, or of the last
// rule on a kind listed before it (Kinds), so a new kind's rules go where
// its kind does; -1 when there is none.
func lastOfKind(s Set, kind string) int {
	rank := slices.Index(Kinds, kind)
	last := -1
	for i, r := range s {
		if k := slices.Index(Kinds, r.On); k <= rank {
			last = i
		}
	}
	return last
}
