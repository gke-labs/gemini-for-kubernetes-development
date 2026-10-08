package fanout

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// markerRe matches a child's marker: <!-- factory:fanout parent=N item=key -->
// for an item's child, <!-- factory:fanout parent=N final --> for the last.
var markerRe = regexp.MustCompile(`<!--\s*factory:fanout\s+parent=(\d+)\s+(?:item=([a-z0-9-]+)|(final))\s*-->`)

// ParseMarker reads a child's marker from its body: its parent, and its item's
// key or that it is the final child.
func ParseMarker(body string) (parent int, key string, final, ok bool) {
	m := markerRe.FindStringSubmatch(body)
	if m == nil {
		return 0, "", false, false
	}
	parent, _ = strconv.Atoi(m[1])
	return parent, m[2], m[3] != "", true
}

// ChildTitle is the title of an item's child.
func (s Spec) ChildTitle(parent int, parentTitle string, it Item) string {
	return renderChecked("title", s.Settings.Title, childData(parent, parentTitle, []Item{it}))
}

// ChildBody is the body of an item's child: the task for the item, the
// item's line, and the marker that ties it to the parent.
func (s Spec) ChildBody(parent int, parentTitle string, it Item) string {
	task := strings.TrimSpace(renderChecked("## Task", s.Task, childData(parent, parentTitle, []Item{it})))
	return fmt.Sprintf("%s\n\n### Item\n- %s\n\nPart of #%d.\n<!-- factory:fanout parent=%d item=%s -->\n", task, it.Line, parent, parent, it.Key)
}

// renderChecked renders a template Parse or LoadItems already rendered for
// every item, so it cannot fail; were it to, the template's text stands in.
func renderChecked(name, text string, data any) string {
	out, err := render(name, text, data)
	if err != nil {
		return text
	}
	return out
}

// FinalTitle is the title of the final child.
func (s Spec) FinalTitle(parentTitle string) string {
	return "Finally: " + parentTitle
}

// FinalBody is the body of the final child.
func (s Spec) FinalBody(parent int) string {
	return fmt.Sprintf("%s\n\nEvery item of #%d is done.\n\nPart of #%d.\n<!-- factory:fanout parent=%d final -->\n", s.Finally, parent, parent, parent)
}

// sameText reports whether an issue's text is what it would be written as.
// GitHub hands bodies back with CRLFs and without trailing whitespace kept
// reliably, so both are compared normalised.
func sameText(a, b string) bool {
	norm := func(s string) string { return strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n")) }
	return norm(a) == norm(b)
}
