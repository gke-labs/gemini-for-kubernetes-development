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
		b.WriteString("\n")
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

func stateIcon(state string) string {
	switch state {
	case StepSucceeded:
		return "✅ Succeeded"
	case StepSkipped:
		return "⏭️ Skipped"
	case StepActive:
		return "🔄 Active"
	case StepReady:
		return "▶️ Ready"
	}
	return "⏳ Pending"
}
