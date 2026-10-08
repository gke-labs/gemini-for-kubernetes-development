package fanout

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"text/template"
)

// render executes a Go text/template. A field the data does not have is an
// error (missingkey=error), so that a typo fails the spec instead of writing
// "<no value>" into every child. Only the built-in functions exist.
func render(name, text string, data any) (string, error) {
	t, err := template.New(name).Option("missingkey=error").Parse(text)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	if err := t.Execute(&b, data); err != nil {
		return "", err
	}
	return b.String(), nil
}

// childData is what the Task and the title are rendered with: the child's
// first item, all of its items, and the parent.
func childData(parent int, parentTitle string, items []Item) map[string]any {
	all := make([]any, len(items))
	for i, it := range items {
		all[i] = it.Fields
	}
	var first any
	if len(all) > 0 {
		first = all[0]
	}
	return map[string]any{
		"item":   first,
		"items":  all,
		"parent": map[string]any{"number": parent, "title": parentTitle},
	}
}

// checkTemplates renders the Task and the title for every item, so a broken
// template or a field some item lacks is a spec error, never a broken child.
// The parent is not known when a spec is parsed; its fields exist all the
// same, and that is all rendering can fail on.
func (s Spec) checkTemplates() error {
	for _, t := range []struct{ name, text string }{{"## Task", s.Task}, {"title", s.Settings.Title}} {
		for _, old := range []string{"{item}", "{parent}"} {
			if strings.Contains(t.text, old) {
				return fmt.Errorf("%s: %s is not a placeholder any more; write {{.item.name}} or {{.parent.title}}", t.name, old)
			}
		}
		for _, it := range s.Items {
			if _, err := render(t.name, t.text, childData(0, "", []Item{it})); err != nil {
				return fmt.Errorf("%s, for item %q: %w", t.name, it.Name, err)
			}
		}
	}
	return nil
}

// LoadItems reads the items from a JSON file's content, as the spec's
// items section says: the array at Select, the elements Where keeps, each
// named by Name. source says where the file was read, for the progress
// comment.
func (s *Spec) LoadItems(data []byte, source string) error {
	src := s.Settings.Items
	if src == nil {
		return fmt.Errorf("the spec reads no items from a file")
	}
	var doc any
	if err := json.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("%s: %w", src.From, err)
	}
	arr, err := selectPath(doc, src.Select)
	if err != nil {
		return fmt.Errorf("%s: %w", src.From, err)
	}

	items := []Item{}
	seen := map[string]string{}
	for i, el := range arr {
		fields := map[string]any{}
		switch v := el.(type) {
		case map[string]any:
			fields = maps.Clone(v)
		case string:
			fields["name"] = v
		default:
			return fmt.Errorf("%s: element %d is neither an object nor a string", src.From, i)
		}
		if src.Where != "" {
			keep, err := render("items.where", src.Where, fields)
			if err != nil {
				return fmt.Errorf("items.where, for element %d: %w", i, err)
			}
			switch strings.TrimSpace(keep) {
			case "true":
			case "false":
				continue
			default:
				return fmt.Errorf("items.where, for element %d: rendered %q, want true or false", i, keep)
			}
		}
		name, err := render("items.name", src.Name, fields)
		if err != nil {
			return fmt.Errorf("items.name, for element %d: %w", i, err)
		}
		name = strings.TrimSpace(name)
		if name == "" {
			return fmt.Errorf("items.name, for element %d: renders empty", i)
		}
		key := itemKey(name)
		if key == "" {
			return fmt.Errorf("item %q: no name to make a key from", name)
		}
		if prev, dup := seen[key]; dup {
			return fmt.Errorf("items %q and %q have the same key %q", prev, name, key)
		}
		seen[key] = name
		fields["name"] = name
		items = append(items, Item{Key: key, Name: name, Line: name, Fields: fields})
	}
	s.Items = items
	s.Source = source
	return s.checkTemplates()
}

// selectPath walks a dotted path (".a.b"; "" or "." is the document) to an
// array.
func selectPath(doc any, path string) ([]any, error) {
	cur := doc
	walked := ""
	for _, part := range strings.Split(strings.Trim(path, "."), ".") {
		if part == "" {
			continue
		}
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s is not an object", or(walked, "the file"))
		}
		walked += "." + part
		if cur, ok = obj[part]; !ok {
			return nil, fmt.Errorf("no %s", walked)
		}
	}
	arr, ok := cur.([]any)
	if !ok {
		return nil, fmt.Errorf("%s is not an array", or(walked, "the file"))
	}
	return arr, nil
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
