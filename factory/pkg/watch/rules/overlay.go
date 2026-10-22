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
	// SourceRepo is a repository's rules, .agents/watch/<name>.yaml.
	SourceRepo = "repo"
)

// IsToggle reports whether the rule file only turns a rule of its name on
// or off: a name and enabled, and nothing else a rule says. A repository
// turns factory's rule off in code with one:
//
//	name: review
//	enabled: false
func (r *Rule) IsToggle() bool {
	return r.Enabled != nil && r.On == "" && r.Order == 0 && r.Exclusive == nil &&
		reflect.ValueOf(r.When).IsZero() && reflect.ValueOf(r.Run).IsZero() && reflect.ValueOf(r.Policy).IsZero()
}

// Load reads a directory of rule files, one per rule, each named after its
// rule (<name>.yaml), as a repository's .agents/watch/ holds them. The
// rules are not validated: Overlay and Augment do, once they are laid over
// the rules they replace or toggle.
func Load(fsys fs.FS) ([]*Rule, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		if files[e.Name()], err = fs.ReadFile(fsys, e.Name()); err != nil {
			return nil, err
		}
	}
	return LoadFiles(files)
}

// LoadFiles is Load on files already read, by file name; a name that is
// not <rule>.yaml is left out. In file name order.
func LoadFiles(files map[string][]byte) ([]*Rule, error) {
	names := make([]string, 0, len(files))
	for file := range files {
		names = append(names, file)
	}
	sort.Strings(names)
	var out []*Rule
	for _, file := range names {
		name, ok := strings.CutSuffix(file, ".yaml")
		if !ok {
			continue
		}
		r, err := Parse(files[file])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		if r.Name != name {
			return nil, fmt.Errorf("%s: the rule is named %s; a rule's file is named after it", file, r.Name)
		}
		out = append(out, r)
	}
	return out, nil
}

// Compose lays a repository's rules over base as mode says, the
// repository recipe mode (spec.repoRecipe.mode) the watch also reads its
// rules by: off (or empty) is base alone; augment adds the repository's
// rules of new names only (Augment); replace lays them over base by name
// (Overlay). skipped names the repository's rules augment left out, and
// why.
func Compose(base Set, repo []*Rule, mode string) (set Set, skipped []string, err error) {
	switch mode {
	case "", "off":
		return base, nil, nil
	case "augment":
		return Augment(base, repo)
	case "replace":
		set, err := Overlay(base, repo)
		return set, nil, err
	default:
		return nil, nil, fmt.Errorf("repository rule mode %q is not one of: off, augment, replace", mode)
	}
}

// Augment adds a repository's rules of names base does not have: a rule of
// one of base's names, or a toggle, is left out (skipped, with why) - in
// augment mode the repository adds rules of its own and changes none of
// factory's. The result is validated, in name order.
func Augment(base Set, repo []*Rule) (Set, []string, error) {
	var add []*Rule
	var skipped []string
	for _, r := range repo {
		switch {
		case base.Named(r.Name) != nil:
			skipped = append(skipped, fmt.Sprintf("%s: factory has a rule of that name, which only replace mode changes", r.Name))
		case r.IsToggle():
			skipped = append(skipped, fmt.Sprintf("%s: turns on or off a rule factory does not have", r.Name))
		default:
			add = append(add, r)
		}
	}
	set, err := Overlay(base, add)
	return set, skipped, err
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
