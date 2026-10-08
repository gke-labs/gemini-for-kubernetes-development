package fanout

import (
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestTemplates(t *testing.T) {
	spec, err := Parse("## Fan-out\ntitle: \"{{.item.name}} ({{.parent.title}} #{{.parent.number}})\"\n\n" +
		"## Task\nFix {{.item.name}}.{{if eq .item.name \"b\" \"c\"}} Carefully.{{end}}\n\n## Items\n- [ ] **a** (one)\n- [ ] b\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := spec.ChildTitle(7, "P", spec.Items[:1]); got != "a (P #7)" {
		t.Errorf("ChildTitle() = %q", got)
	}
	if got := spec.ChildBody(7, "P", spec.Items[:1]); !strings.HasPrefix(got, "Fix a.\n\n### Items\n- **a** (one)\n") {
		t.Errorf("ChildBody(a) = %q", got)
	}
	if got := spec.ChildBody(7, "P", spec.Items[1:2]); !strings.HasPrefix(got, "Fix b. Carefully.\n") {
		t.Errorf("ChildBody(b) = %q", got)
	}
}

func TestTemplateErrors(t *testing.T) {
	tests := []struct{ name, md, want string }{
		{"a missing field", "## Task\nFix {{.item.kidn}}.\n## Items\n- [ ] a\n", "kidn"},
		{"a broken template", "## Task\nFix {{.item.name\n## Items\n- [ ] a\n", "## Task"},
		{"the old placeholder", "## Task\nFix {item}.\n## Items\n- [ ] a\n", "{{.item.name}}"},
		{"the old placeholder in the title", "## Fan-out\ntitle: \"{parent}: x\"\n## Task\nx\n## Items\n- [ ] a\n", "title"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(tc.md)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Parse() error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

const fileSpec = "## Fan-out\n```yaml\nitems:\n  from: kinds.json\n%s```\n\n## Task\n%s\n"

func TestLoadItems(t *testing.T) {
	const kinds = `{"resources": [
		{"kind": "ApigeeInstance", "status": "todo", "beta": true},
		{"kind": "BigQueryDataset", "status": "done"},
		{"kind": "KMSKeyHandle", "status": "todo"}
	]}`
	tests := []struct {
		name     string
		settings string
		task     string
		file     string
		want     []string // names
		wantErr  string
	}{
		{
			name:     "select, where and name",
			settings: "  select: .resources\n  where: '{{ne .status \"done\"}}'\n  name: \"{{.kind}}\"\n",
			task:     "Fix {{.item.kind}}.{{if index .item \"beta\"}} Beta.{{end}}",
			file:     kinds,
			want:     []string{"ApigeeInstance", "KMSKeyHandle"},
		},
		{
			name: "an array of strings, named by default",
			task: "Fix {{.item.name}}.",
			file: `["a", "b"]`,
			want: []string{"a", "b"},
		},
		{
			name:     "a field some element lacks",
			settings: "  select: .resources\n  name: \"{{.kind}}\"\n",
			task:     "{{.item.beta}}",
			file:     kinds,
			wantErr:  "beta",
		},
		{name: "not JSON", file: "nope", task: "x", wantErr: "kinds.json"},
		{name: "no such path", settings: "  select: .items\n", file: kinds, task: "x", wantErr: "no .items"},
		{name: "not an array", file: kinds, task: "x", wantErr: "the file is not an array"},
		{name: "where neither true nor false", settings: "  where: yes\n", file: `["a"]`, task: "x", wantErr: "want true or false"},
		{name: "an empty name", settings: "  name: \"{{if false}}x{{end}}\"\n", file: `["a"]`, task: "x", wantErr: "renders empty"},
		{name: "the same key twice", file: `["Foo Bar", "foo-bar"]`, task: "x", wantErr: "same key"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spec, err := Parse(fmtSpec(tc.settings, tc.task))
			if err != nil {
				t.Fatal(err)
			}
			if spec.Items != nil {
				t.Fatalf("Parse() read items before the file: %v", spec.Items)
			}
			err = spec.LoadItems([]byte(tc.file), "`kinds.json` at abc")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("LoadItems() error = %v, want it to mention %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, it := range spec.Items {
				got = append(got, it.Name)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("items (-want +got):\n%s", diff)
			}
		})
	}
}

func TestItemsFileBody(t *testing.T) {
	spec, err := Parse(fmtSpec("  name: \"{{.kind}}\"\n", "Fix {{.item.kind}} in {{.item.file}}."))
	if err != nil {
		t.Fatal(err)
	}
	if err := spec.LoadItems([]byte(`[{"kind": "A", "file": "a.go"}]`), ""); err != nil {
		t.Fatal(err)
	}
	body := spec.ChildBody(9, "P", spec.Items[:1])
	if !strings.HasPrefix(body, "Fix A in a.go.\n\n### Items\n- A\n") {
		t.Errorf("ChildBody() = %q", body)
	}
	if _, keys, _, _ := ParseMarker(body); !slices.Equal(keys, []string{"a"}) {
		t.Errorf("marker keys %q, want [a]", keys)
	}
}

func TestItemsSourceErrors(t *testing.T) {
	tests := []struct{ name, md, want string }{
		{"both a checklist and a file", fmtSpec("", "x") + "## Items\n- [ ] a\n", "both"},
		{"items without from", "## Fan-out\nitems: {select: .a}\n## Task\nx\n", "no from"},
		{"neither", "## Task\nx\n## Fan-out\ncreate: all\n", "items.from"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(tc.md)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Parse() error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
	if !HasSpecHeadings(fmtSpec("", "x")) {
		t.Error("HasSpecHeadings() does not see a spec with an items file")
	}
}

func fmtSpec(settings, task string) string {
	return strings.Replace(strings.Replace(fileSpec, "%s", settings, 1), "%s", task, 1)
}
