package commands

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/graph"
)

// factory graph validate on a template file: a valid one is described, a
// broken one is an error naming the file.
func TestGraphValidateTemplate(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "two.yaml")
	if err := os.WriteFile(good, []byte(`apiVersion: factory.gemini.google.com/v1alpha1
kind: IssueGraph
metadata: {name: two}
spec:
  params:
    - {name: kind, pattern: '^[A-Z][a-z]+$', example: Foo}
  steps:
    - {id: types, title: 'Types for {{ .Params.kind }}', body: b}
    - {id: controller, after: [types], title: 'Controller for {{ .Params.kind }}', body: 'After #{{ (step "types").Issue }}'}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if _, err := validateGraphTemplate(&out, good); err != nil {
		t.Fatal(err)
	}
	if s := out.String(); !strings.Contains(s, "graph two is valid") || !strings.Contains(s, "controller") || !strings.Contains(s, "after types") {
		t.Errorf("output:\n%s", s)
	}
	bad := filepath.Join(dir, "bad.yaml")
	data, _ := os.ReadFile(good)
	if err := os.WriteFile(bad, []byte(strings.Replace(string(data), "after: [types]", "after: [nope]", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := validateGraphTemplate(&out, bad); err == nil || !strings.Contains(err.Error(), "bad.yaml") || !strings.Contains(err.Error(), "nope") {
		t.Errorf("a broken template: %v", err)
	}
}

type fakeLabels map[string]bool

func (f fakeLabels) LabelExists(_ context.Context, name string) (bool, error) { return f[name], nil }

// --repo warns, once each, about the labels a template uses that the
// repository lacks (KCC had no step/mockgcp-alignment or step/release,
// 2026-11-04); with every label there, it says so.
func TestWarnMissingLabels(t *testing.T) {
	spec := &graph.Spec{
		Defaults: graph.Defaults{Labels: []string{"greenfield"}},
		Steps: []graph.Step{
			{ID: "a", Labels: []string{"step/a"}},
			{ID: "b", Labels: []string{"step/b", "greenfield"}},
		},
		Finally: &graph.Step{Labels: []string{"step/release"}},
	}
	var out bytes.Buffer
	if err := warnMissingLabels(context.Background(), &out, fakeLabels{"greenfield": true, "step/a": true}, "o/r", spec); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{`o/r has no label "step/b"`, `o/r has no label "step/release"`} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
	if strings.Count(got, "warning:") != 2 || strings.Contains(got, `"greenfield"`) {
		t.Errorf("want two warnings, none for labels that exist:\n%s", got)
	}
	out.Reset()
	_ = warnMissingLabels(context.Background(), &out, fakeLabels{"greenfield": true, "step/a": true, "step/b": true, "step/release": true}, "o/r", spec)
	if !strings.Contains(out.String(), "o/r has every label the graph uses") {
		t.Errorf("all there: %s", out.String())
	}
}
