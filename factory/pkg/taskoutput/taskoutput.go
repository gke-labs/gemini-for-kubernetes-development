// Package taskoutput is the one shape every task's result takes: a typed
// document naming what it is about (target), where it came from (source)
// and the result itself (spec). A task leaves it in its task directory as
// File; `factory sandbox task output` prints it and `factory apply` acts
// on it — the task stays read-only and whoever applies the result does so
// with their own token, after a human has looked at it if they want one
// to.
package taskoutput

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/tasks"
)

const (
	// File is the task-directory file a task's result is in.
	File = "task-output.yaml"
	// AppliedFile marks, in a task directory, that the task's result was
	// applied: `factory recipe --apply` run again does not pick it up.
	AppliedFile = "task-output.applied"
	APIVersion  = "factory.gemini.google.com/v1alpha1"
)

// Decl declares a task's result: which kind it is and the task-directory
// file the agent wrote it to.
type Decl struct {
	Kind string `yaml:"kind" json:"kind"`
	From string `yaml:"from" json:"from"`
}

// Document is one result.
type Document struct {
	APIVersion string    `yaml:"apiVersion" json:"apiVersion"`
	Kind       string    `yaml:"kind" json:"kind"`
	Target     Target    `yaml:"target" json:"target"`
	Source     Source    `yaml:"source,omitempty" json:"source,omitempty"`
	Spec       yaml.Node `yaml:"spec" json:"-"`
}

// Target is the issue or PR the result is about.
type Target struct {
	URL string `yaml:"url" json:"url"`
}

// Source is the task that produced the result. Task also marks what apply
// writes, so applying a result twice does not write it twice.
type Source struct {
	Sandbox string `yaml:"sandbox,omitempty" json:"sandbox,omitempty"`
	Task    string `yaml:"task,omitempty" json:"task,omitempty"`
	Recipe  string `yaml:"recipe,omitempty" json:"recipe,omitempty"`
	Engine  string `yaml:"engine,omitempty" json:"engine,omitempty"`
}

// kind is what taskoutput knows of one kind of result.
type kind struct {
	// parse turns what the agent wrote into the spec, failing on anything
	// apply could not act on.
	parse func(raw string) (any, error)
}

var kinds = map[string]kind{
	"Triage": {parse: parseTriage},
}

// Known reports whether kind is one taskoutput can wrap and apply.
func Known(k string) bool {
	_, ok := kinds[k]
	return ok
}

// KnownKinds lists them, for error messages.
func KnownKinds() []string {
	var out []string
	for k := range kinds {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Wrap makes the document for what an agent wrote, as kind k.
func Wrap(k, raw string, target Target, source Source) (*Document, error) {
	kd, ok := kinds[k]
	if !ok {
		return nil, fmt.Errorf("unknown task output kind %q (known: %s)", k, strings.Join(KnownKinds(), ", "))
	}
	spec, err := kd.parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%s output: %w", k, err)
	}
	doc := &Document{APIVersion: APIVersion, Kind: k, Target: target, Source: source}
	if err := doc.Spec.Encode(spec); err != nil {
		return nil, err
	}
	return doc, nil
}

// Marshal writes documents as a YAML stream.
func Marshal(docs ...*Document) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	for _, d := range docs {
		if err := enc.Encode(d); err != nil {
			return nil, err
		}
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Parse reads a YAML stream of documents and checks each is one this
// version of factory understands.
func Parse(data []byte) ([]*Document, error) {
	var docs []*Document
	dec := yaml.NewDecoder(bytes.NewReader(data))
	for {
		var d Document
		err := dec.Decode(&d)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parsing task output: %w", err)
		}
		if d.APIVersion != APIVersion {
			return nil, fmt.Errorf("task output apiVersion %q: want %s", d.APIVersion, APIVersion)
		}
		if !Known(d.Kind) {
			return nil, fmt.Errorf("unknown task output kind %q (known: %s)", d.Kind, strings.Join(KnownKinds(), ", "))
		}
		if d.Target.URL == "" {
			return nil, fmt.Errorf("%s task output has no target.url", d.Kind)
		}
		docs = append(docs, &d)
	}
	if len(docs) == 0 {
		return nil, fmt.Errorf("no task output documents")
	}
	return docs, nil
}

// Triage is a Triage document's spec.
type Triage = tasks.TriageSuggestion

// TriageSpec decodes a Triage document's spec.
func (d *Document) TriageSpec() (*Triage, error) {
	if d.Kind != "Triage" {
		return nil, fmt.Errorf("%s task output is not a Triage", d.Kind)
	}
	var t Triage
	if err := d.Spec.Decode(&t); err != nil {
		return nil, fmt.Errorf("Triage spec: %w", err)
	}
	return &t, nil
}

func parseTriage(raw string) (any, error) {
	var out tasks.TriageAgentOutput
	if err := yaml.Unmarshal([]byte(CleanAgentYAML(raw, "triage:")), &out); err != nil {
		return nil, err
	}
	if out.Triage == nil {
		return nil, fmt.Errorf("no triage: block")
	}
	return out.Triage, nil
}

// CleanAgentYAML is the YAML in an agent's reply: without markdown fences
// and without anything said before the line that starts with indicator.
func CleanAgentYAML(raw, indicator string) string {
	s := strings.TrimSpace(raw)
	s = strings.TrimSpace(strings.TrimPrefix(s, "```yaml"))
	s = strings.TrimSpace(strings.TrimSuffix(s, "```"))
	if indicator != "" && !strings.HasPrefix(s, indicator) {
		if i := strings.Index(s, "\n"+indicator); i != -1 {
			s = s[i+1:]
		}
	}
	return s
}
