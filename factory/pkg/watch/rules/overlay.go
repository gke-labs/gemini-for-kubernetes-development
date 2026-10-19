package rules

import (
	"fmt"
	"io/fs"
	"reflect"
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
	return r.Enabled != nil && r.On == "" && r.Order == 0 && r.Exclusive == nil && len(r.Expect) == 0 &&
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

// Overlay lays a repository's rules over base (factory's): a toggle turns
// the base rule of its name on or off, keeping the rest of it; a whole rule
// replaces the base rule of its name, or adds one of a new name. Each
// rule's Source says whose it is, and EnabledBy whose enabled it carries
// when a toggle set it. base is not changed. The result is validated, in
// name order.
//
// Either way round: a repository may turn on a rule factory ships off
// (inactive), as well as off one it ships on. The overseer's operator has
// the last word, on the Overseer (spec.rules), over both.
func Overlay(base Set, repo []*Rule) (Set, error) {
	byName := map[string]int{}
	out := make(Set, 0, len(base)+len(repo))
	for _, r := range base {
		c := *r
		if c.Source == "" {
			c.Source = SourceFactory
		}
		byName[c.Name] = len(out)
		out = append(out, &c)
	}
	for _, r := range repo {
		c := *r
		i, exists := byName[c.Name]
		if c.IsToggle() {
			if !exists {
				return nil, fmt.Errorf("rule %s: turns on or off a rule factory does not have", c.Name)
			}
			out[i].Enabled = c.Enabled
			out[i].EnabledBy = SourceRepo
			continue
		}
		c.Source = SourceRepo
		if exists {
			out[i] = &c
			continue
		}
		byName[c.Name] = len(out)
		out = append(out, &c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	if err := out.Validate(); err != nil {
		return nil, err
	}
	return out, nil
}
