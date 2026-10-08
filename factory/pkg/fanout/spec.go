// Package fanout turns a parent issue that describes one task for many items
// into child issues, labelled for the coder bots a few at a time: a window
// that starts small and grows as children's PRs merge (a slow start).
//
// This package is the pure part: parsing the spec, deciding what a pass does,
// and rendering what it writes. Reading and writing GitHub is the caller's.
// See factory/design/fanout.md.
package fanout

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// SpecMarker marks the comment that holds a parent's spec.
const SpecMarker = "<!-- factory:fanout-spec -->"

// Spec is a fan-out: the task for one item, the items, and the final step.
type Spec struct {
	// Task is what to do for one child, a template (see childData).
	Task string
	// Items are the checklist's unchecked lines, in order, or the elements
	// of the file Settings.Items names, once LoadItems has read it.
	Items []Item
	// Source says where file items were read: the path and the commit.
	Source string
	// Finally is the last child's body, created once every item is done.
	// Empty when there is no final step.
	Finally  string
	Settings Settings
}

// Item is one line of the items checklist, or one element of an items file.
type Item struct {
	// Key identifies the item's child across passes: its name, lowercased,
	// non-alphanumerics folded to '-'.
	Key  string
	Name string
	// Line is the whole line, as written, without its checkbox; a file
	// item's name.
	Line string
	// Fields are what templates see as the item: name and line for a
	// checklist line; the element's own fields, and name, for a file item.
	Fields map[string]any
}

// Settings are the spec's Fan-out section, with the defaults filled in.
type Settings struct {
	// Title is the child's title, a template like the Task.
	Title string
	// Labels go on every child, besides the trigger label.
	Labels []string
	// Create is CreateAll or CreateLazy.
	Create string
	// Group is how many items a child is made with, and how far it grows.
	Group  Window
	Window Window
	// Checkpoints are the numbers of items done at which the fan-out stops.
	Checkpoints []int
	// Items, when set, reads the items from a JSON file instead of the
	// ## Items checklist.
	Items *ItemSource
}

// ItemSource is where a spec's items come from when they are not a
// checklist: a JSON file in the repository, or at an https URL.
type ItemSource struct {
	// From is the file's path, read from the default branch, or an https
	// URL, read as it is on every pass.
	From string `yaml:"from"`
	// Select is a dotted path to the array (".a.b"); empty, the file is it.
	Select string `yaml:"select"`
	// Where, a template rendering true or false, keeps an element or not.
	// Empty keeps every element.
	Where string `yaml:"where"`
	// Name, a template, is an item's name. Default {{.name}}.
	Name string `yaml:"name"`
}

// Window is a ramp's start and its most: of children labelled at once for
// the window, of items per child for the group. Written as one number, it
// is a ramp that does not grow.
type Window struct {
	Start int `yaml:"start"`
	Max   int `yaml:"max"`
}

func (w *Window) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		var v int
		if err := n.Decode(&v); err != nil {
			return err
		}
		*w = Window{Start: v, Max: v}
		return nil
	}
	type plain Window
	return n.Decode((*plain)(w))
}

const (
	// CreateAll creates every child at the start, unlabelled.
	CreateAll = "all"
	// CreateLazy creates a child only when it is labelled.
	CreateLazy = "lazy"

	defaultTitle       = "{{.item.name}}: {{.parent.title}}"
	defaultGroupTitle  = "{{range $i, $it := .items}}{{if $i}}, {{end}}{{$it.name}}{{end}}: {{.parent.title}}"
	defaultItemName    = "{{.name}}"
	defaultWindowStart = 2
	defaultWindowMax   = 8
)

// rawSettings is the YAML as written. Checkpoints is a pointer so that an
// explicit [] (never stop) is told apart from leaving it out (the default).
type rawSettings struct {
	Title       string      `yaml:"title"`
	Labels      []string    `yaml:"labels"`
	Create      string      `yaml:"create"`
	Group       Window      `yaml:"group"`
	Window      Window      `yaml:"window"`
	Checkpoints *[]int      `yaml:"checkpoints"`
	Items       *ItemSource `yaml:"items"`
}

const (
	sectionTask    = "task"
	sectionItems   = "items"
	sectionFinally = "finally"
	sectionFanOut  = "fan-out"
)

var (
	headingRe = regexp.MustCompile(`^##\s+(.+?)\s*#*\s*$`)
	fenceRe   = regexp.MustCompile("^\\s*(```+|~~~+)")
	itemRe    = regexp.MustCompile(`^\s*[-*+]\s+\[([ xX])\]\s+(.+?)\s*$`)
	boldRe    = regexp.MustCompile(`\*\*(.+?)\*\*|__(.+?)__`)
	keyRe     = regexp.MustCompile(`[^a-z0-9]+`)
)

// sectionName is the standard section a level-2 heading names, or "" for
// any other heading.
func sectionName(heading string) string {
	h := strings.ToLower(strings.TrimSpace(heading))
	switch h {
	case sectionTask, sectionItems, sectionFinally, sectionFanOut:
		return h
	case "fanout", "fan out":
		return sectionFanOut
	}
	return ""
}

// sections splits markdown at its level-2 headings, outside code fences, and
// returns the standard sections' text. Other sections are left out.
func sections(markdown string) map[string]string {
	out := map[string]string{}
	var cur string
	var buf []string
	var fence string
	flush := func() {
		if cur != "" {
			out[cur] = strings.TrimSpace(strings.Join(buf, "\n"))
		}
		buf = nil
	}
	for _, line := range strings.Split(strings.ReplaceAll(markdown, "\r\n", "\n"), "\n") {
		if m := fenceRe.FindStringSubmatch(line); m != nil {
			switch {
			case fence == "":
				fence = m[1]
			case strings.HasPrefix(m[1], fence[:1]) && len(m[1]) >= len(fence):
				fence = ""
			}
		} else if fence == "" {
			if m := headingRe.FindStringSubmatch(line); m != nil {
				flush()
				cur = sectionName(m[1])
				continue
			}
		}
		if cur != "" {
			buf = append(buf, line)
		}
	}
	flush()
	return out
}

// HasSpecHeadings reports whether markdown is written as a spec: it has a
// Task section, and an Items section or a Fan-out section (which can name an
// items file). Parse says whether the spec is a good one.
func HasSpecHeadings(markdown string) bool {
	s := sections(markdown)
	_, task := s[sectionTask]
	_, items := s[sectionItems]
	_, fanOut := s[sectionFanOut]
	return task && (items || fanOut)
}

// Parse reads a spec from markdown with the standard headings: ## Task,
// ## Items, and optionally ## Finally and ## Fan-out. When the Fan-out
// section names an items file instead, the spec has no items until the
// caller reads the file and hands it to LoadItems.
func Parse(markdown string) (Spec, error) {
	s := sections(markdown)
	var spec Spec
	spec.Task = s[sectionTask]
	if spec.Task == "" {
		return Spec{}, fmt.Errorf("no ## Task section, or it is empty")
	}
	spec.Finally = s[sectionFinally]
	settings, err := parseSettings(s[sectionFanOut])
	if err != nil {
		return Spec{}, err
	}
	spec.Settings = settings
	_, hasItems := s[sectionItems]
	switch {
	case settings.Items != nil && hasItems:
		return Spec{}, fmt.Errorf("both a ## Items section and items.from: keep one")
	case settings.Items != nil:
		return spec, nil
	case !hasItems:
		return Spec{}, fmt.Errorf("no ## Items section, and no items.from in ## Fan-out")
	}
	seen := map[string]string{}
	lines := 0
	for _, line := range strings.Split(s[sectionItems], "\n") {
		m := itemRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		lines++
		if m[1] != " " {
			continue
		}
		it := Item{Line: m[2], Name: itemName(m[2])}
		it.Key = itemKey(it.Name)
		it.Fields = map[string]any{"name": it.Name, "line": it.Line}
		if it.Key == "" {
			return Spec{}, fmt.Errorf("item %q: no name to make a key from", it.Line)
		}
		if prev, dup := seen[it.Key]; dup {
			return Spec{}, fmt.Errorf("items %q and %q have the same key %q", prev, it.Name, it.Key)
		}
		seen[it.Key] = it.Name
		spec.Items = append(spec.Items, it)
	}
	if lines == 0 {
		return Spec{}, fmt.Errorf("## Items has no checklist lines (- [ ] item)")
	}
	if err := spec.checkTemplates(); err != nil {
		return Spec{}, err
	}
	return spec, nil
}

// itemKey is the key of an item named name.
func itemKey(name string) string {
	return strings.Trim(keyRe.ReplaceAllString(strings.ToLower(name), "-"), "-")
}

// itemName is an item line's bold text if it has any, otherwise the line up
// to the first " (" or " - ".
func itemName(line string) string {
	if m := boldRe.FindStringSubmatch(line); m != nil {
		return strings.TrimSpace(m[1] + m[2])
	}
	name := line
	for _, sep := range []string{" (", " - "} {
		if i := strings.Index(name, sep); i > 0 {
			name = name[:i]
		}
	}
	return strings.TrimSpace(name)
}

// parseSettings reads the Fan-out section's YAML block, or the section as
// YAML if it has no fence, and fills in the defaults.
func parseSettings(section string) (Settings, error) {
	var raw rawSettings
	if text := yamlText(section); strings.TrimSpace(text) != "" {
		dec := yaml.NewDecoder(bytes.NewBufferString(text))
		dec.KnownFields(true)
		if err := dec.Decode(&raw); err != nil {
			return Settings{}, fmt.Errorf("## Fan-out: %w", err)
		}
	}
	s := Settings{Title: raw.Title, Labels: raw.Labels, Create: raw.Create, Group: raw.Group, Window: raw.Window, Items: raw.Items}
	if s.Items != nil {
		if strings.TrimSpace(s.Items.From) == "" {
			return Settings{}, fmt.Errorf("## Fan-out: items has no from")
		}
		if isItemsURL(s.Items.From) {
			if err := checkItemsURL(s.Items.From); err != nil {
				return Settings{}, fmt.Errorf("## Fan-out: %w", err)
			}
		}
		if s.Items.Name == "" {
			s.Items.Name = defaultItemName
		}
	}
	if s.Group.Start == 0 {
		s.Group.Start = 1
	}
	if s.Group.Max == 0 {
		s.Group.Max = s.Group.Start
	}
	if s.Group.Start < 1 || s.Group.Max < s.Group.Start {
		return Settings{}, fmt.Errorf("## Fan-out: group {start: %d, max: %d}: want 1 <= start <= max", s.Group.Start, s.Group.Max)
	}
	grouped := s.Group.Max > 1
	if s.Title == "" {
		s.Title = defaultTitle
		if grouped {
			s.Title = defaultGroupTitle
		}
	}
	switch s.Create {
	case "":
		s.Create = CreateLazy
	case CreateAll:
		if grouped {
			return Settings{}, fmt.Errorf("## Fan-out: create: all with group above 1: a group is made when its child is labelled, so use create: lazy")
		}
	case CreateLazy:
	default:
		return Settings{}, fmt.Errorf("## Fan-out: create is %q, want %q or %q", s.Create, CreateAll, CreateLazy)
	}
	if s.Window.Start == 0 {
		s.Window.Start = defaultWindowStart
	}
	if s.Window.Max == 0 {
		s.Window.Max = max(defaultWindowMax, s.Window.Start)
	}
	if s.Window.Start < 1 || s.Window.Max < s.Window.Start {
		return Settings{}, fmt.Errorf("## Fan-out: window {start: %d, max: %d}: want 1 <= start <= max", s.Window.Start, s.Window.Max)
	}
	if raw.Checkpoints == nil {
		s.Checkpoints = []int{s.Group.Start * s.Window.Start}
	} else {
		s.Checkpoints = *raw.Checkpoints
	}
	for _, c := range s.Checkpoints {
		if c < 1 {
			return Settings{}, fmt.Errorf("## Fan-out: checkpoint %d: want 1 or more", c)
		}
	}
	return s, nil
}

// yamlText is the first fenced block's content, or the whole section when it
// has no fence.
func yamlText(section string) string {
	lines := strings.Split(section, "\n")
	for i, line := range lines {
		m := fenceRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		var body []string
		for _, l := range lines[i+1:] {
			if c := fenceRe.FindStringSubmatch(l); c != nil && strings.HasPrefix(c[1], m[1][:1]) && len(c[1]) >= len(m[1]) {
				break
			}
			body = append(body, l)
		}
		return strings.Join(body, "\n")
	}
	return section
}
