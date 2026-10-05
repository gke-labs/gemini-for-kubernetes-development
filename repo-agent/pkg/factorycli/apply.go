package factorycli

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// TaskOutputAPIVersion is the task output documents' apiVersion.
const TaskOutputAPIVersion = "factory.gemini.google.com/v1alpha1"

// ComposeTaskOutput is the task output factory apply takes for a draft:
// the document the task left, as stored without its spec (header), with
// the draft, as the member may have edited it, as its spec. A draft
// stored before task outputs were kept has no header; it gets a fresh
// one, whose source task is task — something stable for the draft, which
// is what factory dedups its comments on.
//
// A Triage draft is a triage: block; a Plan draft is the plan's markdown.
func ComposeTaskOutput(kind, header, draft, issueURL, task string) (string, error) {
	spec, err := draftSpec(kind, draft)
	if err != nil {
		return "", err
	}
	return composeTaskOutput(kind, header, spec, issueURL, task)
}

// ComposeNotes is the Notes task output factory apply --action push-notes
// takes for a research conversation's notes draft: as ComposeTaskOutput,
// with the note's file name, the conversation's (empty leaves it to
// factory: the session's id).
func ComposeNotes(header, markdown, name, repoURL, task string) (string, error) {
	if strings.TrimSpace(markdown) == "" {
		return "", fmt.Errorf("the notes are empty")
	}
	spec := map[string]string{"markdown": strings.TrimSpace(markdown)}
	if name != "" {
		spec["name"] = name
	}
	return composeTaskOutput("Notes", header, spec, repoURL, task)
}

func composeTaskOutput(kind, header string, spec any, targetURL, task string) (string, error) {
	root := headerNode(kind, header)
	if root == nil {
		root = &yaml.Node{Kind: yaml.MappingNode}
		setKey(root, "apiVersion", scalar(TaskOutputAPIVersion))
		setKey(root, "kind", scalar(kind))
		setKey(root, "source", mapping("task", task))
	}
	if !hasTarget(root) {
		setKey(root, "target", mapping("url", targetURL))
	}
	specNode := &yaml.Node{}
	if err := specNode.Encode(spec); err != nil {
		return "", fmt.Errorf("encoding the %s's spec: %w", kind, err)
	}
	setKey(root, "spec", specNode)
	out, err := yaml.Marshal(root)
	if err != nil {
		return "", fmt.Errorf("encoding the %s: %w", kind, err)
	}
	return string(out), nil
}

// draftSpec is a draft as its kind's spec.
func draftSpec(kind, draft string) (any, error) {
	switch kind {
	case "Plan":
		if strings.TrimSpace(draft) == "" {
			return nil, fmt.Errorf("the plan is empty")
		}
		return map[string]string{"markdown": strings.TrimSpace(draft)}, nil
	case "Triage":
		var d struct {
			Triage struct {
				Labels     []string `yaml:"labels"`
				Priority   string   `yaml:"priority"`
				Duplicates []any    `yaml:"duplicates"`
				Assessment string   `yaml:"assessment"`
			} `yaml:"triage"`
		}
		if err := yaml.Unmarshal([]byte(draft), &d); err != nil {
			return nil, fmt.Errorf("the triage is not YAML: %w", err)
		}
		t := d.Triage
		spec := map[string]any{"assessment": t.Assessment}
		if len(t.Labels) > 0 {
			spec["labels"] = t.Labels
		}
		if t.Priority != "" {
			spec["priority"] = t.Priority
		}
		// factory takes issue numbers; an edited draft may say "#12".
		var dups []int
		for _, v := range t.Duplicates {
			if n, err := strconv.Atoi(strings.TrimPrefix(strings.TrimSpace(fmt.Sprint(v)), "#")); err == nil && n > 0 {
				dups = append(dups, n)
			}
		}
		if len(dups) > 0 {
			spec["duplicates"] = dups
		}
		return spec, nil
	}
	return nil, fmt.Errorf("no task output of kind %q", kind)
}

// headerNode is a stored document of kind as a mapping, or nil.
func headerNode(kind, header string) *yaml.Node {
	if _, ok := parseTaskOutputMeta(kind, header); !ok {
		return nil
	}
	var doc yaml.Node
	if yaml.Unmarshal([]byte(header), &doc) != nil || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil
	}
	return doc.Content[0]
}

func hasTarget(root *yaml.Node) bool {
	var d struct {
		Target struct {
			URL string `yaml:"url"`
		} `yaml:"target"`
	}
	return root.Decode(&d) == nil && d.Target.URL != ""
}

// setKey sets key in a mapping node, replacing it where it is or
// appending it.
func setKey(m *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = value
			return
		}
	}
	m.Content = append(m.Content, scalar(key), value)
}

func scalar(v string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Value: v} }

func mapping(key, value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{scalar(key), scalar(value)}}
}

// ApplyOptions are the inputs for a `factory apply --action` invocation.
type ApplyOptions struct {
	// Doc is the task output to apply (ComposeTaskOutput).
	Doc string
	// Action is the write: label, comment or push-notes.
	Action      string
	GithubToken string
	Timeout     time.Duration
}

// StartApply runs `factory apply -f <doc> --action <action>`: one write
// of a task output to its issue, with the member's token. It runs here,
// not in a sandbox — a label or a comment is a GitHub call, and the
// document is the only input. Both writes are idempotent in factory
// (labels add; a comment carries its task's marker), so a retry after a
// restart repeats nothing.
func (r *Runner) StartApply(key string, opts ApplyOptions) bool {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	if r.IsRunning(key) {
		return false
	}
	// A file, because command has no stdin. Removed once the run is
	// over, whatever came of it.
	f, err := os.CreateTemp("", "task-output-*.yaml")
	if err != nil {
		r.fail(key, fmt.Errorf("writing the task output: %w", err))
		return true
	}
	_, werr := f.WriteString(opts.Doc)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(f.Name())
		r.fail(key, fmt.Errorf("writing the task output: %w", werr))
		return true
	}
	args := []string{"apply", "-f", f.Name(), "--action", opts.Action}
	started := r.startWithPreflight(key, args, opts.GithubToken, timeout, &preflight{
		harvest: func(_ context.Context, out string, err error) (string, error) {
			os.Remove(f.Name())
			return out, err
		},
	})
	if !started {
		os.Remove(f.Name())
	}
	return started
}

// fail records a result for key without running anything.
func (r *Runner) fail(key string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.results[key] = Result{Err: err, FinishedAt: time.Now()}
}
