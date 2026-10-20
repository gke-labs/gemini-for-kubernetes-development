package rules

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io"

	"gopkg.in/yaml.v3"
)

// The prebaked rules: factory's own, one file per kind of item, listing
// its rules in the order the engine walks them (design: Where rules live).
// An overseer with no rules of its own runs them all.
//
//go:embed *.yaml
var builtinFS embed.FS

// Kinds are the kinds of item in the order Builtin lists them.
var Kinds = []string{OnIssue, OnPR, OnSchedule, OnEvent}

// kindFile is a file of factory's rules: one kind, its rules in order.
type kindFile struct {
	On    string `yaml:"on"`
	Rules []Rule `yaml:"rules"`
}

// Builtin is the prebaked rule set, validated, in the order the engine
// walks it: issue.yaml, pr.yaml, schedule.yaml, event.yaml, each top to
// bottom.
func Builtin() (Set, error) {
	var set Set
	for _, kind := range Kinds {
		name := kind + ".yaml"
		data, err := builtinFS.ReadFile(name)
		if err != nil {
			return nil, err
		}
		rs, err := parseKindFile(data, kind)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		for _, r := range rs {
			r.Source = SourceFactory
			set = append(set, r)
		}
	}
	if err := set.Validate(); err != nil {
		return nil, err
	}
	return set, nil
}

// parseKindFile reads a kind's file, strictly: its on is kind, each rule
// takes it, and a rule is placed by its position, not by before or after.
func parseKindFile(data []byte, kind string) ([]*Rule, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var f kindFile
	if err := dec.Decode(&f); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("empty rules file")
		}
		return nil, err
	}
	if f.On != kind {
		return nil, fmt.Errorf("on is %q, want %q (the file is named after its kind)", f.On, kind)
	}
	out := make([]*Rule, 0, len(f.Rules))
	for i := range f.Rules {
		r := f.Rules[i]
		switch {
		case r.On != "":
			return nil, fmt.Errorf("rule %s: on is the file's", r.Name)
		case r.Before != "" || r.After != "":
			return nil, fmt.Errorf("rule %s: a rule in a kind's file is placed by its position", r.Name)
		}
		r.On = kind
		out = append(out, &r)
	}
	return out, nil
}
