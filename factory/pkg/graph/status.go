package graph

import (
	"fmt"
	"strings"
)

// statusMarker marks the watcher's status comment on a parent.
var statusMarker = fmt.Sprintf("<!-- factory:comment key=%s v=1 -->", StatusKey)

// IsStatusComment is whether body is a graph status comment.
func IsStatusComment(body string) bool { return strings.Contains(body, statusMarker) }

// RenderStatus is the parent's status comment: the phase, a row per step,
// and the state block. It has no timestamps of its own, so it changes only
// when something real does.
func RenderStatus(source string, version int, s *Spec, r Result) string {
	var b strings.Builder
	finished := 0
	for _, st := range r.Steps {
		if done(st.State) {
			finished++
		}
	}
	fmt.Fprintf(&b, "### Issue graph - %s · %d/%d done", r.Phase, finished, len(r.Steps))
	if r.Reason != "" {
		fmt.Fprintf(&b, " · %s", r.Reason)
	}
	b.WriteString("\n\n")
	if s != nil && len(r.Steps) > 0 {
		b.WriteString(renderDiagram(s, r))
		b.WriteString("\n<details><summary>Steps</summary>\n\n")
		b.WriteString("| Step | After | Then | Issue | State |\n|---|---|---|---|---|\n")
		for _, st := range r.Steps {
			step := s.Step(st.ID)
			after, then := "-", ThenFix
			if step != nil {
				then = step.ThenOrDefault()
				if len(step.After) > 0 {
					after = strings.Join(step.After, ", ")
				}
			}
			if st.ID == FinallyID {
				after = "every step"
			}
			issue := "-"
			if st.Child > 0 {
				issue = fmt.Sprintf("#%d", st.Child)
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n", st.ID, after, then, issue, stateIcon(st.State))
		}
		b.WriteString("\n</details>\n\n")
	}
	if len(r.Orphans) > 0 {
		var refs []string
		for _, n := range r.Orphans {
			refs = append(refs, fmt.Sprintf("#%d", n))
		}
		fmt.Fprintf(&b, "> Orphaned (their steps are no longer in the definition): %s\n\n", strings.Join(refs, ", "))
	}
	steps := "steps: " + source
	if source == SourcePlan && version > 0 {
		steps = fmt.Sprintf("steps: plan v%d", version)
	}
	if s != nil {
		steps += fmt.Sprintf(" · maxActive %d", s.ActiveLimit())
	}
	fmt.Fprintf(&b, "<sub>%s</sub>\n\n%s\n%s", steps, r.State.Block(), statusMarker)
	return b.String()
}

// renderDiagram is the graph as a mermaid flowchart, which GitHub draws in
// a comment: a node per step - green when done, blue when active, yellow
// when its work is in its group's PR, dashed grey when not started - with
// its issue, the after edges between them, and, for a grouped graph, a box
// around each group's steps: one PR. Nodes link to their issues relative to
// the parent's page, where the renderer allows links.
func renderDiagram(s *Spec, r Result) string {
	node := map[string]string{}
	state := map[string]StepStatus{}
	for i, st := range r.Steps {
		node[st.ID] = fmt.Sprintf("n%d", i)
		state[st.ID] = st
	}
	var b strings.Builder
	b.WriteString("```mermaid\nflowchart LR\n")
	define := func(id, indent string) {
		st := state[id]
		label := diagramIcon(st.State) + id
		var notes []string
		if st.Child > 0 {
			notes = append(notes, fmt.Sprintf("#%d", st.Child))
		}
		switch st.State {
		case StepActive:
			notes = append(notes, "active")
		case StepInGroup:
			notes = append(notes, "in the PR")
		case StepSkipped:
			notes = append(notes, "skipped")
		case StepReady:
			notes = append(notes, "ready")
		}
		if step := s.Step(id); step != nil && step.GroupGate && r.Grouped {
			notes = append(notes, "gate")
		}
		if len(notes) > 0 {
			label += "<br/><small>" + strings.Join(notes, " · ") + "</small>"
		}
		open, closing := "[\"", "\"]"
		if id == FinallyID {
			open, closing = "([\"", "\"])" // rounded: the closing step
		}
		fmt.Fprintf(&b, "%s%s%s%s%s:::%s\n", indent, node[id], open, label, closing, diagramClass(st.State))
	}
	defined := map[string]bool{}
	if r.Grouped {
		g := 0
		for _, st := range s.Steps {
			if s.GroupPrev(st.ID) != "" || s.GroupNext(st.ID) == "" {
				continue // not the first of a group of two or more
			}
			var members []string
			for id := st.ID; id != ""; id = s.GroupNext(id) {
				members = append(members, id)
			}
			title, merged, pr := "one PR", true, 0
			for _, id := range members {
				merged = merged && state[id].State == StepSucceeded
				if state[id].PR > 0 {
					pr = state[id].PR
				}
			}
			if pr > 0 {
				title = fmt.Sprintf("PR #%d", pr)
			}
			if merged {
				title += " · merged"
			}
			fmt.Fprintf(&b, "  subgraph g%d[\"%s\"]\n    direction LR\n", g, title)
			for _, id := range members {
				define(id, "    ")
				defined[id] = true
			}
			b.WriteString("  end\n")
			cls := "pr"
			if merged {
				cls = "prmerged"
			}
			fmt.Fprintf(&b, "  class g%d %s\n", g, cls)
			g++
		}
	}
	for _, st := range r.Steps {
		if !defined[st.ID] {
			define(st.ID, "  ")
		}
	}
	// The after edges; finally follows the steps no other step follows.
	followed := map[string]bool{}
	for _, st := range s.Steps {
		for _, a := range st.After {
			if node[a] != "" && node[st.ID] != "" {
				fmt.Fprintf(&b, "  %s --> %s\n", node[a], node[st.ID])
				followed[a] = true
			}
		}
	}
	if s.Finally != nil && node[FinallyID] != "" {
		for _, st := range s.Steps {
			if !followed[st.ID] {
				fmt.Fprintf(&b, "  %s --> %s\n", node[st.ID], node[FinallyID])
			}
		}
	}
	for _, st := range r.Steps {
		if st.Child > 0 {
			fmt.Fprintf(&b, "  click %s \"%d\"\n", node[st.ID], st.Child)
		}
	}
	b.WriteString(`  classDef done fill:#dafbe1,stroke:#1a7f37,color:#116329,stroke-width:2px
  classDef active fill:#ddf4ff,stroke:#0969da,color:#0550ae,stroke-width:3px
  classDef ingroup fill:#fff8c5,stroke:#9a6700,color:#7d4e00,stroke-width:2px
  classDef ready fill:#f6f8fa,stroke:#0969da,color:#0550ae,stroke-dasharray:4 3
  classDef pending fill:#f6f8fa,stroke:#8c959f,color:#59636e,stroke-dasharray:4 3
  classDef skipped fill:#f6f8fa,stroke:#8c959f,color:#8c959f
  classDef pr fill:#fffdf0,stroke:#9a6700,stroke-dasharray:3 3
  classDef prmerged fill:#f6fff8,stroke:#1a7f37
`)
	b.WriteString("```\n")
	return b.String()
}

func diagramIcon(state string) string {
	switch state {
	case StepSucceeded:
		return "✓ "
	case StepSkipped:
		return "⏭ "
	case StepActive:
		return "● "
	case StepInGroup:
		return "📦 "
	}
	return ""
}

func diagramClass(state string) string {
	switch state {
	case StepSucceeded:
		return "done"
	case StepSkipped:
		return "skipped"
	case StepActive:
		return "active"
	case StepInGroup:
		return "ingroup"
	case StepReady:
		return "ready"
	}
	return "pending"
}

func stateIcon(state string) string {
	switch state {
	case StepSucceeded:
		return "✅ Succeeded"
	case StepSkipped:
		return "⏭️ Skipped"
	case StepActive:
		return "🔄 Active"
	case StepInGroup:
		return "📦 In the group's PR"
	case StepReady:
		return "▶️ Ready"
	}
	return "⏳ Pending"
}
