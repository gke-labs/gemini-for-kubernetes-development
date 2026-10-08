package fanout

import (
	"fmt"
	"strings"
)

// Progress renders the progress comment: one row per item with its child,
// PR and state, the window, and the state block. children are as they are
// after the pass, what it created and labelled included.
func Progress(in Input, st State, children []Child) string {
	v := newView(Input{Spec: in.Spec, Children: children, TriggerLabel: in.TriggerLabel})
	done, _ := v.done()
	active := 0
	for _, c := range v.itemChildren() {
		if c.Open && c.Labelled {
			active++
		}
	}

	var b strings.Builder
	b.WriteString(ProgressMarker + "\n")
	fmt.Fprintf(&b, "### Fan-out progress\n\n%d of %d done · %d in progress · ", done, len(in.Spec.Items), active)
	if g := in.Spec.Settings.Group; g.Max > 1 {
		fmt.Fprintf(&b, "group %d (max %d) · ", st.Group, g.Max)
	}
	fmt.Fprintf(&b, "window %d (max %d)", st.Window, in.Spec.Settings.Window.Max)
	if in.Stopped {
		fmt.Fprintf(&b, " · stopped: remove `%s` to continue", in.StopLabel)
	}
	if in.Spec.Source != "" {
		fmt.Fprintf(&b, "\n\nItems from %s.", in.Spec.Source)
	}
	b.WriteString("\n\n| Item | Child | PR | State |\n|---|---|---|---|\n")
	for _, it := range in.Spec.Items {
		c, ok := v.byKey[it.Key]
		if !ok {
			fmt.Fprintf(&b, "| %s | — | — | not created |\n", cell(it.Name))
			continue
		}
		fmt.Fprintf(&b, "| %s | #%d | %s | %s |\n", cell(it.Name), c.Number, prCell(c), childState(c))
	}
	if in.Spec.Finally != "" {
		if c := v.final; c != nil {
			fmt.Fprintf(&b, "| *Finally* | #%d | %s | %s |\n", c.Number, prCell(*c), childState(*c))
		} else {
			b.WriteString("| *Finally* | — | — | after every item |\n")
		}
	}
	b.WriteString("\n" + st.block() + "\n")
	return b.String()
}

func childState(c Child) string {
	switch {
	case !c.Open && c.NotPlanned:
		return "skipped"
	case !c.Open:
		return "done"
	case c.Labelled:
		return "in progress"
	}
	return "waiting"
}

func prCell(c Child) string {
	pr := mainPR(c)
	if pr == nil {
		return "—"
	}
	switch {
	case pr.Merged:
		return fmt.Sprintf("#%d merged", pr.Number)
	case pr.Open:
		return fmt.Sprintf("#%d open", pr.Number)
	}
	return fmt.Sprintf("#%d closed", pr.Number)
}

// cell keeps a name from breaking the table.
func cell(s string) string { return strings.ReplaceAll(s, "|", `\|`) }
