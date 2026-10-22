package rules

import (
	"embed"
	"fmt"
	"sort"
	"strings"
)

// The prebaked rules: factory's own, one file per rule, describing what
// today's watch does (design: Where rules live). An overseer with no rules
// of its own runs them all.
//
//go:embed *.yaml
var builtinFS embed.FS

// IsBuiltinName reports whether name is one of the prebaked rules' names:
// the rules the scanners carry out by name. Any other rule (a
// repository's) runs as a rule task (api.TypePRRule, api.TypeIssueRule).
func IsBuiltinName(name string) bool {
	if name == "" || strings.ContainsAny(name, "/.") {
		return false
	}
	_, err := builtinFS.ReadFile(name + ".yaml")
	return err == nil
}

// Builtin is the prebaked rule set, validated, in name order.
func Builtin() (Set, error) {
	entries, err := builtinFS.ReadDir(".")
	if err != nil {
		return nil, err
	}
	var set Set
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".yaml")
		if !ok {
			continue
		}
		data, err := builtinFS.ReadFile(e.Name())
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
		r.Source = SourceFactory
		set = append(set, r)
	}
	sort.Slice(set, func(i, j int) bool { return set[i].Name < set[j].Name })
	if err := set.Validate(); err != nil {
		return nil, err
	}
	return set, nil
}
