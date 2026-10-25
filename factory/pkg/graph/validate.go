package graph

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"text/template"
)

var (
	stepIDRE    = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	paramNameRE = regexp.MustCompile(`^[a-z][a-zA-Z0-9_]*$`)
)

// Validate checks a template: its spec, its params, and that every step
// renders with each param's example and with every optional param left
// out. A template may not replan (a checkpoint replan is a plan's: a
// template has no plan to revise).
func (t *Template) Validate() error {
	if err := t.Spec.validate(false); err != nil {
		return err
	}
	if err := t.Spec.validateParams(); err != nil {
		return err
	}
	return t.Spec.checkRenders()
}

// ValidatePlan checks a plan's steps: the spec (no params), and that every
// step renders.
func (s *Spec) ValidatePlan() error {
	if err := s.validate(true); err != nil {
		return err
	}
	return s.checkRenders()
}

// validate checks the steps: ids, after edges (known, not itself, no
// cycle), then and checkpoint values, maxActive, and the finally step.
func (s *Spec) validate(plan bool) error {
	if s.MaxActive < 0 {
		return fmt.Errorf("maxActive %d: give 1 or more (or leave it out, for 1)", s.MaxActive)
	}
	if len(s.Steps) == 0 {
		return errors.New("no steps")
	}
	seen := map[string]bool{}
	for i, st := range s.Steps {
		where := fmt.Sprintf("steps[%d]", i)
		if st.ID != "" {
			where = fmt.Sprintf("step %s", st.ID)
		}
		switch {
		case st.ID == "":
			return fmt.Errorf("%s: no id", where)
		case st.ID == FinallyID:
			return fmt.Errorf("%s: %q is the finally step's; give the step another id, or make it finally:", where, FinallyID)
		case !stepIDRE.MatchString(st.ID):
			return fmt.Errorf("%s: the id is not lowercase letters, digits and dashes, starting with a letter", where)
		case seen[st.ID]:
			return fmt.Errorf("%s: the id is given twice", where)
		}
		seen[st.ID] = true
		if err := st.validateFields(plan); err != nil {
			return fmt.Errorf("%s: %w", where, err)
		}
	}
	for _, st := range s.Steps {
		for _, a := range st.After {
			switch {
			case a == st.ID:
				return fmt.Errorf("step %s: after itself", st.ID)
			case a == FinallyID:
				return fmt.Errorf("step %s: after finally, which comes after every step", st.ID)
			case !seen[a]:
				return fmt.Errorf("step %s: after %s, which is not a step", st.ID, a)
			}
		}
	}
	if cycle := s.cycle(); cycle != nil {
		return fmt.Errorf("the steps' after edges form a cycle: %s", strings.Join(cycle, " -> "))
	}
	if f := s.Finally; f != nil {
		if f.ID != "" && f.ID != FinallyID {
			return fmt.Errorf("finally: has an id of its own (%s); it is the finally step", f.ID)
		}
		if len(f.After) > 0 {
			return errors.New("finally: has after; it comes after every step")
		}
		if err := f.validateFields(plan); err != nil {
			return fmt.Errorf("finally: %w", err)
		}
	}
	return nil
}

// validateFields checks a step's own fields.
func (st Step) validateFields(plan bool) error {
	if strings.TrimSpace(st.Title) == "" {
		return errors.New("no title")
	}
	switch st.Then {
	case "", ThenFix, ThenPlan, ThenAsk:
	default:
		return fmt.Errorf("then %q is not one of %s, %s, %s", st.Then, ThenFix, ThenPlan, ThenAsk)
	}
	for i, a := range st.Checkpoint {
		switch a {
		case CheckpointStop:
		case CheckpointReplan:
			if !plan {
				return errors.New("checkpoint replan is a plan's: a template has no plan to revise")
			}
		default:
			return fmt.Errorf("checkpoint %q is not one of %s, %s", a, CheckpointStop, CheckpointReplan)
		}
		if slices.Contains(st.Checkpoint[:i], a) {
			return fmt.Errorf("checkpoint %s is given twice", a)
		}
	}
	for _, l := range st.Labels {
		if strings.TrimSpace(l) == "" {
			return errors.New("an empty label")
		}
	}
	return nil
}

// cycle is a cycle of after edges, as the ids along it (the first again at
// the end); nil for none.
func (s *Spec) cycle() []string {
	const (
		unvisited = iota
		visiting
		done
	)
	state := map[string]int{}
	after := map[string][]string{}
	for _, st := range s.Steps {
		after[st.ID] = st.After
	}
	var path []string
	var visit func(id string) []string
	visit = func(id string) []string {
		switch state[id] {
		case visiting:
			i := slices.Index(path, id)
			return append(slices.Clone(path[i:]), id)
		case done:
			return nil
		}
		state[id] = visiting
		path = append(path, id)
		for _, a := range after[id] {
			if c := visit(a); c != nil {
				return c
			}
		}
		path = path[:len(path)-1]
		state[id] = done
		return nil
	}
	for _, st := range s.Steps {
		if c := visit(st.ID); c != nil {
			return c
		}
	}
	return nil
}

// Upstream is every step id must wait for, directly or not.
func (s *Spec) Upstream(id string) map[string]bool {
	out := map[string]bool{}
	if id == FinallyID {
		for _, st := range s.Steps {
			out[st.ID] = true
		}
		return out
	}
	var walk func(string)
	walk = func(id string) {
		st := s.Step(id)
		if st == nil {
			return
		}
		for _, a := range st.After {
			if !out[a] {
				out[a] = true
				walk(a)
			}
		}
	}
	walk(id)
	return out
}

// validateParams checks a template's params: names, a pattern or an enum
// each, defaults only on optional params, and defaults and examples that
// satisfy the constraint.
func (s *Spec) validateParams() error {
	seen := map[string]bool{}
	for i, p := range s.Params {
		where := fmt.Sprintf("params[%d]", i)
		if p.Name != "" {
			where = "param " + p.Name
		}
		switch {
		case !paramNameRE.MatchString(p.Name):
			return fmt.Errorf("%s: the name is not letters, digits and underscores, starting with a lowercase letter", where)
		case seen[p.Name]:
			return fmt.Errorf("%s: the name is given twice", where)
		case p.Pattern == "" && len(p.Enum) == 0:
			return fmt.Errorf("%s: give a pattern or an enum, so free text never reaches a template", where)
		case p.Default != "" && p.IsRequired():
			return fmt.Errorf("%s: a default is for an optional param (required: false)", where)
		}
		seen[p.Name] = true
		if p.Pattern != "" {
			if _, err := regexp.Compile(p.Pattern); err != nil {
				return fmt.Errorf("%s: the pattern: %w", where, err)
			}
		}
		for _, v := range []struct{ what, value string }{{"default", p.Default}, {"example", p.Example}} {
			if v.value == "" {
				continue
			}
			if err := p.Check(v.value); err != nil {
				return fmt.Errorf("%s: the %s: %w", where, v.what, err)
			}
		}
		if p.Pattern != "" && len(p.Enum) == 0 && p.Example == "" {
			return fmt.Errorf("%s: give an example, which the steps are rendered with to check them", where)
		}
	}
	return nil
}

// Check is whether value satisfies the param's constraint.
func (p Param) Check(value string) error {
	if len(p.Enum) > 0 && !slices.Contains(p.Enum, value) {
		return fmt.Errorf("%q is not one of %s", value, strings.Join(p.Enum, ", "))
	}
	if p.Pattern != "" {
		re, err := regexp.Compile(p.Pattern)
		if err != nil {
			return err
		}
		if !re.MatchString(value) {
			return fmt.Errorf("%q does not match %s", value, p.Pattern)
		}
	}
	return nil
}

// sample is the value Validate renders a param with: its example, else its
// first enum value.
func (p Param) sample() string {
	if p.Example != "" {
		return p.Example
	}
	if len(p.Enum) > 0 {
		return p.Enum[0]
	}
	return p.Default
}

// checkRenders renders every step's title, body and review with each
// param's sample, and again with every optional param left out (its
// default, or empty), so a broken template or a step it may not reference
// is a definition error, never a broken child.
func (s *Spec) checkRenders() error {
	full, omitted := map[string]string{}, map[string]string{}
	for _, p := range s.Params {
		full[p.Name] = p.sample()
		if p.IsRequired() {
			omitted[p.Name] = p.sample()
		} else {
			omitted[p.Name] = p.Default
		}
	}
	steps := slices.Clone(s.Steps)
	if s.Finally != nil {
		f := *s.Finally
		f.ID = FinallyID
		steps = append(steps, f)
	}
	for _, params := range []map[string]string{full, omitted} {
		for _, st := range steps {
			for _, field := range []struct{ name, text string }{{"title", st.Title}, {"body", st.Body}, {"review", st.Review}} {
				if field.text == "" {
					continue
				}
				if _, err := s.Render(st.ID, field.text, params, nil); err != nil {
					return fmt.Errorf("step %s: the %s: %w", st.ID, field.name, err)
				}
			}
		}
	}
	return nil
}

// StepRef is what a template's `step "<id>"` gives: the child issue of a
// step this one depends on.
type StepRef struct {
	Issue int
	URL   string
}

// Render renders one of step id's templates with params and the children
// made so far (by step id). `step "<id>"` gives the child of a step id
// depends on, directly or not, and is an error for any other step; a
// missing field is an error (missingkey=error).
func (s *Spec) Render(id, text string, params map[string]string, children map[string]StepRef) (string, error) {
	upstream := s.Upstream(id)
	funcs := template.FuncMap{
		"step": func(ref string) (StepRef, error) {
			if !upstream[ref] {
				return StepRef{}, fmt.Errorf("step %q: not a step %s comes after", ref, id)
			}
			return children[ref], nil
		},
	}
	t, err := template.New(id).Option("missingkey=error").Funcs(funcs).Parse(text)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	if err := t.Execute(&b, map[string]any{"Params": params}); err != nil {
		return "", err
	}
	return b.String(), nil
}
