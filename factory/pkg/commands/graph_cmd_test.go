package commands

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	if err := validateGraphTemplate(&out, good); err != nil {
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
	if err := validateGraphTemplate(&out, bad); err == nil || !strings.Contains(err.Error(), "bad.yaml") || !strings.Contains(err.Error(), "nope") {
		t.Errorf("a broken template: %v", err)
	}
}
