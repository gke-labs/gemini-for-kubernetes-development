package fanout

import (
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

const kmsSpec = SpecMarker + `
## Fan-out
` + "```yaml" + `
title: "Migrate {{.item.name}} to kmsv1beta1.KMSCryptoKeyRef"
labels: []
create: all
window: {start: 2, max: 8}
checkpoints: [2]
` + "```" + `

## Task
For ` + "`{{.item.name}}`" + `, switch its KMS reference.

## Items
- [ ] **ApigeeInstance** (` + "`apis/apigee/v1alpha1/instance_types.go`" + `)
- [x] **AlloyDBCluster** (already done)
- [ ] BigQueryDataset (` + "`apis/bigquery/v1beta1/bigquerydataset_types.go`" + `)
- [ ] Some thing - with a dash
not an item

## Finally
Remove ` + "`refs.KMSCryptoKeyRef`" + `.
`

func TestParse(t *testing.T) {
	spec, err := Parse(kmsSpec)
	if err != nil {
		t.Fatal(err)
	}
	want := Spec{
		Task: "For `{{.item.name}}`, switch its KMS reference.",
		Items: []Item{
			{Key: "apigeeinstance", Name: "ApigeeInstance", Line: "**ApigeeInstance** (`apis/apigee/v1alpha1/instance_types.go`)"},
			{Key: "bigquerydataset", Name: "BigQueryDataset", Line: "BigQueryDataset (`apis/bigquery/v1beta1/bigquerydataset_types.go`)"},
			{Key: "some-thing", Name: "Some thing", Line: "Some thing - with a dash"},
		},
		Finally: "Remove `refs.KMSCryptoKeyRef`.",
		Settings: Settings{
			Title:       "Migrate {{.item.name}} to kmsv1beta1.KMSCryptoKeyRef",
			Labels:      []string{},
			Create:      CreateAll,
			Group:       Window{Start: 1, Max: 1},
			Window:      Window{Start: 2, Max: 8},
			Checkpoints: []int{2},
		},
	}
	for i, it := range want.Items {
		want.Items[i].Fields = map[string]any{"name": it.Name, "line": it.Line}
	}
	if diff := cmp.Diff(want, spec); diff != "" {
		t.Errorf("Parse() (-want +got):\n%s", diff)
	}
}

func TestParseDefaults(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want Settings
	}{
		{
			name: "no section",
			want: Settings{Title: defaultTitle, Create: CreateAll, Group: Window{Start: 1, Max: 1}, Window: Window{Start: 2, Max: 8}, Checkpoints: []int{2}},
		},
		{
			name: "start only, checkpoint follows it",
			yaml: "window: {start: 3}",
			want: Settings{Title: defaultTitle, Create: CreateAll, Group: Window{Start: 1, Max: 1}, Window: Window{Start: 3, Max: 8}, Checkpoints: []int{3}},
		},
		{
			name: "start above the default max",
			yaml: "window: {start: 10}",
			want: Settings{Title: defaultTitle, Create: CreateAll, Group: Window{Start: 1, Max: 1}, Window: Window{Start: 10, Max: 10}, Checkpoints: []int{10}},
		},
		{
			name: "no checkpoints",
			yaml: "create: lazy\ncheckpoints: []",
			want: Settings{Title: defaultTitle, Create: CreateLazy, Group: Window{Start: 1, Max: 1}, Window: Window{Start: 2, Max: 8}, Checkpoints: []int{}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			md := "## Task\ndo {{.item.name}}\n\n## Items\n- [ ] a\n"
			if tc.yaml != "" {
				md += "\n## Fan-out\n```yaml\n" + tc.yaml + "\n```\n"
			}
			spec, err := Parse(md)
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tc.want, spec.Settings); diff != "" {
				t.Errorf("settings (-want +got):\n%s", diff)
			}
		})
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name, md, want string
	}{
		{"no task", "## Items\n- [ ] a\n", "Task"},
		{"no items section", "## Task\nx\n", "Items"},
		{"no checklist", "## Task\nx\n## Items\n- a\n", "checklist"},
		{"duplicate key", "## Task\nx\n## Items\n- [ ] Foo Bar\n- [ ] foo-bar\n", "same key"},
		{"bad create", "## Task\nx\n## Items\n- [ ] a\n## Fan-out\ncreate: some\n", "create"},
		{"bad window", "## Task\nx\n## Items\n- [ ] a\n## Fan-out\nwindow: {start: 4, max: 2}\n", "window"},
		{"unknown setting", "## Task\nx\n## Items\n- [ ] a\n## Fan-out\nwindows: 2\n", "windows"},
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

func TestHeadingsInFencesAreText(t *testing.T) {
	md := "## Task\nRun:\n```sh\n## Items\n```\n\n## Items\n- [ ] a\n"
	spec, err := Parse(md)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(spec.Task, "## Items") || len(spec.Items) != 1 {
		t.Errorf("got task %q, items %v", spec.Task, spec.Items)
	}
	if HasSpecHeadings("## Task\n```\n## Items\n```\n") {
		t.Error("HasSpecHeadings() saw a heading inside a fence")
	}
}

func TestChildBodyMarker(t *testing.T) {
	spec, err := Parse(kmsSpec)
	if err != nil {
		t.Fatal(err)
	}
	body := spec.ChildBody(13781, "KMS refs", spec.Items[:1])
	if !strings.HasPrefix(body, "For `ApigeeInstance`, switch") {
		t.Errorf("body does not start with the task for the item:\n%s", body)
	}
	parent, key, final, ok := ParseMarker(body)
	if !ok || parent != 13781 || !slices.Equal(key, []string{"apigeeinstance"}) || final {
		t.Errorf("ParseMarker() = %d %q %v %v", parent, key, final, ok)
	}
	parent, key, final, ok = ParseMarker(spec.FinalBody(13781))
	if !ok || parent != 13781 || key != nil || !final {
		t.Errorf("ParseMarker(final) = %d %q %v %v", parent, key, final, ok)
	}
	if got := spec.ChildTitle(13781, "KMS refs", spec.Items[:1]); got != "Migrate ApigeeInstance to kmsv1beta1.KMSCryptoKeyRef" {
		t.Errorf("ChildTitle() = %q", got)
	}
}

func TestStateRoundTrip(t *testing.T) {
	st := State{Window: 3, Group: 1, Counted: []int{1201}, Checkpoints: []int{2}, Children: map[string]int{"a": 1201}, Started: []int{1201}}
	in := Input{Spec: Spec{Items: []Item{{Key: "a", Name: "a"}}, Settings: Settings{Window: Window{Start: 2, Max: 8}}}}
	got, ok := ParseState(Progress(in, st, nil))
	if !ok {
		t.Fatal("ParseState() found no state")
	}
	if diff := cmp.Diff(st, *got); diff != "" {
		t.Errorf("state (-want +got):\n%s", diff)
	}
}
