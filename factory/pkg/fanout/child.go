package fanout

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// markerRe matches a child's marker: <!-- factory:fanout parent=N items=a,b -->
// for an items' child, <!-- factory:fanout parent=N final --> for the last.
var markerRe = regexp.MustCompile(`<!--\s*factory:fanout\s+parent=(\d+)\s+(?:items=([a-z0-9-]+(?:,[a-z0-9-]+)*)|(final))\s*-->`)

// ParseMarker reads a child's marker from its body: its parent, and its
// items' keys or that it is the final child.
func ParseMarker(body string) (parent int, keys []string, final, ok bool) {
	m := markerRe.FindStringSubmatch(body)
	if m == nil {
		return 0, nil, false, false
	}
	parent, _ = strconv.Atoi(m[1])
	if m[2] != "" {
		keys = strings.Split(m[2], ",")
	}
	return parent, keys, m[3] != "", true
}

// ChildTitle is the title of the child for items.
func (s Spec) ChildTitle(parent int, parentTitle string, items []Item) string {
	return renderChecked("title", s.Settings.Title, childData(parent, parentTitle, items))
}

// ChildBody is the body of the child for items: the task for them, their
// lines under ### Items (one or many), and the marker that ties the child to the parent.
func (s Spec) ChildBody(parent int, parentTitle string, items []Item) string {
	task := strings.TrimSpace(renderChecked("## Task", s.Task, childData(parent, parentTitle, items)))
	var lines strings.Builder
	for _, it := range items {
		fmt.Fprintf(&lines, "- %s\n", it.Line)
	}
	return fmt.Sprintf("%s\n\n### Items\n%s\nPart of #%d.\n<!-- factory:fanout parent=%d items=%s -->\n",
		task, lines.String(), parent, parent, strings.Join(itemKeys(items), ","))
}

// itemKeys are the keys of items, in order.
func itemKeys(items []Item) []string {
	keys := make([]string, len(items))
	for i, it := range items {
		keys[i] = it.Key
	}
	return keys
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
