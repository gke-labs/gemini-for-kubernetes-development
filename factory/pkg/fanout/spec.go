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
	// Task is what to do for one item; {item} is replaced by its name.
	Task string
	// Items are the checklist's unchecked lines, in order.
	Items []Item
	// Finally is the last child's body, created once every item is done.
	// Empty when there is no final step.
	Finally  string
	Settings Settings
}

// Item is one line of the items checklist.
type Item struct {
	// Key identifies the item's child across passes: its name, lowercased,
	// non-alphanumerics folded to '-'.
	Key  string
	Name string
	// Line is the whole line, as written, without its checkbox.
	Line string
}

// Settings are the spec's Fan-out section, with the defaults filled in.
type Settings struct {
	// Title is the child's title; {item} and {parent} are replaced.
	Title string
	// Labels go on every child, besides the trigger label.
	Labels []string
	// Create is CreateAll or CreateBatch.
	Create string
	Window Window
	// Checkpoints are the numbers of items done at which the fan-out stops.
	Checkpoints []int
}

// Window is the slow start's: how many children are labelled at first, and
// at most.
type Window struct {
	Start int `yaml:"start"`
	Max   int `yaml:"max"`
}

const (
	// CreateAll creates every child at the start, unlabelled.
	CreateAll = "all"
	// CreateBatch creates a child only when it is labelled.
	CreateBatch = "batch"

	defaultTitle       = "{item}: {parent}"
	defaultWindowStart = 2
	defaultWindowMax   = 8
)

// rawSettings is the YAML as written. Checkpoints is a pointer so that an
// explicit [] (never stop) is told apart from leaving it out (the default).
type rawSettings struct {
	Title       string   `yaml:"title"`
	Labels      []string `yaml:"labels"`
	Create      string   `yaml:"create"`
	Window      Window   `yaml:"window"`
	Checkpoints *[]int   `yaml:"checkpoints"`
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
// Task and an Items section. Parse says whether the spec is a good one.
func HasSpecHeadings(markdown string) bool {
	s := sections(markdown)
	_, task := s[sectionTask]
	_, items := s[sectionItems]
	return task && items
}

// Parse reads a spec from markdown with the standard headings: ## Task,
// ## Items, and optionally ## Finally and ## Fan-out.
func Parse(markdown string) (Spec, error) {
	s := sections(markdown)
	var spec Spec
	spec.Task = s[sectionTask]
	if spec.Task == "" {
		return Spec{}, fmt.Errorf("no ## Task section, or it is empty")
	}
	if _, ok := s[sectionItems]; !ok {
		return Spec{}, fmt.Errorf("no ## Items section")
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
		it.Key = strings.Trim(keyRe.ReplaceAllString(strings.ToLower(it.Name), "-"), "-")
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
	spec.Finally = s[sectionFinally]
	settings, err := parseSettings(s[sectionFanOut])
	if err != nil {
		return Spec{}, err
	}
	spec.Settings = settings
	return spec, nil
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
	s := Settings{Title: raw.Title, Labels: raw.Labels, Create: raw.Create, Window: raw.Window}
	if s.Title == "" {
		s.Title = defaultTitle
	}
	switch s.Create {
	case "":
		s.Create = CreateAll
	case CreateAll, CreateBatch:
	default:
		return Settings{}, fmt.Errorf("## Fan-out: create is %q, want %q or %q", s.Create, CreateAll, CreateBatch)
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
		s.Checkpoints = []int{s.Window.Start}
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
