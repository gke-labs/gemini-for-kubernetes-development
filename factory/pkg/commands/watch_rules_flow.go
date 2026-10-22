package commands

import (
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/watch/rules"
)

// printRulesFlow draws how the rules on one kind of item are applied: the
// engine's gates (rules.Gates), then every rule in the order the engine walks them, with its
// condition, what holds it back and what it does.
func printRulesFlow(out io.Writer, set rules.Set, kind string) error {
	switch kind {
	case rules.OnPR, rules.OnIssue:
	default:
		return fmt.Errorf("--flow %q: want pr or issue", kind)
	}
	item := map[string]string{rules.OnPR: "PR", rules.OnIssue: "issue"}[kind]
	fmt.Fprintf(out, "%s rules, walked top to bottom on every scan\n\n", strings.ToUpper(item[:1])+item[1:])
	if kind == rules.OnPR || kind == rules.OnIssue {
		first := true
		for _, g := range rules.Gates {
			if !slices.Contains(g.Kinds, kind) {
				continue
			}
			label := ""
			if first {
				label, first = "gates", false
			}
			then := "no rule decides"
			if g.Drops {
				then += ", its queued tasks dropped"
			}
			fmt.Fprintf(out, "  %-6s %-17s the %s %s: %s\n", label, g.Name, item, g.Why, then)
		}
		fmt.Fprintf(out, "  ●      takes the %s: of these, the first that decides wins, and none\n", item)
		fmt.Fprintf(out, "         decides while a task of the %s is queued or running\n", item)
	}
	fmt.Fprintf(out, "  ↓      a rule whose when fails, or whose policy holds it back, passes the %s on\n\n", item)

	n := 0
	line := func(label, text string) { fmt.Fprintf(out, "        %-8s %s\n", label, text) }
	for _, r := range set.Ordered() {
		if r.On != kind {
			continue
		}
		n++
		mark := " "
		if r.IsExclusive() {
			mark = "●"
		}
		place := "   "
		if r.Order > 0 {
			place = fmt.Sprintf("%3d", r.Order)
		}
		head := fmt.Sprintf("  %s %s %s", place, mark, r.Name)
		var notes []string
		if !r.IsEnabled() {
			notes = append(notes, "off")
		}
		if r.Source == rules.SourceRepo {
			notes = append(notes, "the repository's")
		}
		if len(notes) > 0 {
			head += "  (" + strings.Join(notes, ", ") + ")"
		}
		fmt.Fprintln(out, head)
		line("when", orDash(r.When.String()))
		if held := flowHeld(r.Policy); held != "" {
			line("held", held)
		}
		if g := r.Policy.GiveUp; g != nil {
			line("give up", flowGiveUp(g))
		}
		line("then", flowRun(r))
		fmt.Fprintln(out, "          ↓")
	}
	if n == 0 {
		fmt.Fprintf(out, "  (no rules on %s)\n", kind)
		return nil
	}
	fmt.Fprintf(out, "        nothing more on this scan\n")
	return nil
}

// flowHeld is what holds a rule back once its condition holds: its dedupe
// and cooldown.
func flowHeld(p rules.Policy) string {
	var parts []string
	switch p.Dedupe {
	case "head":
		parts = append(parts, "once per head")
	case "update":
		parts = append(parts, "once per change to the item")
	}
	if c := p.Cooldown; c != nil {
		var waits []string
		if c.Success.IsSet() {
			waits = append(waits, c.Success.String()+" after a success")
		}
		if c.Failure.IsSet() {
			waits = append(waits, c.Failure.String()+" after a failure")
		}
		s := "wait " + strings.Join(waits, ", ")
		var lifts []string
		if len(c.ResetOn) > 0 {
			lifts = append(lifts, "a new "+strings.Join(c.ResetOn, " or ")+" ends it")
		}
		if c.SkipWhen != nil {
			lifts = append(lifts, "not when "+c.SkipWhen.String())
		}
		if len(lifts) > 0 {
			s += " (" + strings.Join(lifts, "; ") + ")"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, "; ")
}

func flowGiveUp(g *rules.GiveUp) string {
	s := fmt.Sprintf("after %d runs", g.Attempts)
	if len(g.Since) > 0 {
		s += " since " + strings.Join(g.Since, ", ")
	}
	s += " → the stop label"
	if g.Comment != "" {
		s += " + " + fmt.Sprintf("%q", g.Comment)
	}
	return s
}

// flowRun is what a rule does when it decides.
func flowRun(r *rules.Rule) string {
	run := r.Run
	if run.Recipe != "" {
		s := "run " + run.Recipe
		if run.Apply != "" {
			s += " → " + run.Apply
		}
		return s
	}
	var parts []string
	if len(run.Label) > 0 {
		parts = append(parts, "label "+strings.Join(run.Label, ", "))
	}
	if len(run.Unlabel) > 0 {
		parts = append(parts, "unlabel "+strings.Join(run.Unlabel, ", "))
	}
	if run.Assign != "" {
		parts = append(parts, "assign "+run.Assign)
	}
	if run.Unassign != "" {
		parts = append(parts, "unassign "+run.Unassign)
	}
	if run.Comment != "" {
		parts = append(parts, fmt.Sprintf("comment %q", run.Comment))
	}
	if run.Stop != nil {
		parts = append(parts, fmt.Sprintf("the stop label + %q → the walk ends", run.Stop.Comment))
	}
	if run.Drop {
		parts = append(parts, "drop the queued tasks")
	}
	s := strings.Join(parts, "; ")
	if !r.IsExclusive() && (len(run.Label) > 0 || len(run.Unlabel) > 0) {
		s += " (later rules see the labels)"
	}
	return s
}
