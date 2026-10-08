package fanout

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestParseGroup(t *testing.T) {
	tests := []struct {
		name, yaml string
		want       Settings
		wantErr    string
	}{
		{
			name: "a growing group: lazy, a title for many, checkpoint the first batch",
			yaml: "group: {start: 2, max: 5}\nwindow: {start: 3}",
			want: Settings{Title: defaultGroupTitle, Create: CreateLazy, Group: Window{Start: 2, Max: 5}, Window: Window{Start: 3, Max: 8}, Checkpoints: []int{6}},
		},
		{
			name: "one number is a group that does not grow",
			yaml: "group: 5\ncreate: lazy",
			want: Settings{Title: defaultGroupTitle, Create: CreateLazy, Group: Window{Start: 5, Max: 5}, Window: Window{Start: 2, Max: 8}, Checkpoints: []int{10}},
		},
		{name: "create: all", yaml: "group: {start: 1, max: 5}\ncreate: all", wantErr: "create: lazy"},
		{name: "start above max", yaml: "group: {start: 4, max: 2}", wantErr: "group"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spec, err := Parse("## Task\ndo {{.item.name}}\n\n## Items\n- [ ] a\n- [ ] b\n\n## Fan-out\n```yaml\n" + tc.yaml + "\n```\n")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Parse() error = %v, want it to mention %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tc.want, spec.Settings); diff != "" {
				t.Errorf("settings (-want +got):\n%s", diff)
			}
		})
	}
}

func TestGroupChild(t *testing.T) {
	spec, err := Parse("## Task\nMigrate {{range .items}}{{.name}} {{end}}together.\n\n## Items\n- [ ] **A** (a.go)\n- [ ] B\n\n## Fan-out\ngroup: 2\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := spec.ChildTitle(7, "P", spec.Items); got != "A, B: P" {
		t.Errorf("ChildTitle() = %q", got)
	}
	body := spec.ChildBody(7, "P", spec.Items)
	if !strings.HasPrefix(body, "Migrate A B together.\n\n### Items\n- **A** (a.go)\n- B\n\nPart of #7.\n") {
		t.Errorf("ChildBody() = %q", body)
	}
	parent, keys, final, ok := ParseMarker(body)
	if !ok || parent != 7 || final || !slices.Equal(keys, []string{"a", "b"}) {
		t.Errorf("ParseMarker() = %d %q %v %v", parent, keys, final, ok)
	}
}

// TestSyncGroupRamp walks the design's example: the group doubles per child
// completed up to its max, then the window grows by one.
func TestSyncGroupRamp(t *testing.T) {
	ctx := context.Background()
	var items strings.Builder
	for i := 1; i <= 25; i++ {
		fmt.Fprintf(&items, "- [ ] i%02d\n", i)
	}
	body := "## Task\nDo {{range .items}}{{.name}} {{end}}\n\n## Items\n" + items.String() +
		"\n## Fan-out\n```yaml\ngroup: {start: 1, max: 5}\nwindow: {start: 1, max: 3}\ncheckpoints: []\n```\n"
	gh := newFake(body)
	opts := SyncOptions{Issue: parent, TriggerLabel: "overseer", BotLogin: "bot"}
	sync := func() {
		t.Helper()
		if _, err := Sync(ctx, gh, opts); err != nil {
			t.Fatal(err)
		}
	}
	// sizes are the open labelled children's item counts.
	sizes := func() []int {
		var out []int
		for _, n := range gh.labelled("overseer") {
			if c := gh.issues[n]; c.Open {
				_, keys, _, _ := ParseMarker(c.Body)
				out = append(out, len(keys))
			}
		}
		return out
	}

	closeFirst := func() {
		for _, n := range gh.labelled("overseer") {
			if gh.issues[n].Open {
				gh.closeChild(n)
				return
			}
		}
	}

	steps := []struct {
		want []int // sizes after the pass
	}{
		{[]int{1}}, {[]int{2}}, {[]int{4}}, {[]int{5}},
		{[]int{5, 5}}, // group at max: the window grows
	}
	for i, step := range steps {
		if i > 0 {
			closeFirst()
		}
		sync()
		if got := sizes(); !slices.Equal(got, step.want) {
			t.Fatalf("step %d: children in flight %v, want %v", i, got, step.want)
		}
	}
	// One of the two done: window 3, so two more, the last with what is left.
	closeFirst()
	sync()
	if got := sizes(); !slices.Equal(got, []int{5, 3}) {
		t.Fatalf("children in flight %v, want [5 3]", got)
	}
	if progress := gh.comments[parent][0].GetBody(); !strings.Contains(progress, "17 of 25 done · 2 in progress · group 5 (max 5) · window 3 (max 3)") {
		t.Errorf("progress:\n%s", progress)
	}
	if st, _ := ParseState(gh.comments[parent][0].GetBody()); st.Children["i25"] == 0 || st.Children["i25"] != st.Children["i23"] {
		t.Errorf("state children do not map every item of a group to its child: %v", st.Children)
	}
}

func TestDecideGroup(t *testing.T) {
	spec, err := Parse("## Task\nDo it.\n\n## Items\n- [ ] a\n- [ ] b\n- [ ] c\n- [ ] d\n- [ ] e\n- [ ] f\n\n## Fan-out\n" +
		"group: {start: 1, max: 4}\nwindow: {start: 2, max: 4}\ncheckpoints: []\n")
	if err != nil {
		t.Fatal(err)
	}
	child := func(n int, open bool, keys ...string) Child {
		c := Child{Number: n, Keys: keys, Open: open, Labelled: true}
		var its []Item
		for _, it := range spec.Items {
			if slices.Contains(keys, it.Key) {
				its = append(its, it)
			}
		}
		c.Title, c.Body = spec.ChildTitle(parent, "P", its), spec.ChildBody(parent, "P", its)
		return c
	}
	unmerged := func(c Child, pr int) Child { c.PRs = []PR{{Number: pr}}; return c }
	tests := []struct {
		name          string
		st            *State
		children      []Child
		group, window int
		create        []string
	}{
		{name: "start: one child per window slot, a group of start each", group: 1, window: 2, create: []string{"a", "b"}},
		{
			name:     "a child done doubles the group; the window stays",
			st:       &State{Window: 2, Group: 1},
			children: []Child{child(1, false, "a"), child(2, true, "b")},
			group:    2, window: 2, create: []string{"c,d"},
		},
		{
			name:     "an unmerged PR halves the window first",
			st:       &State{Window: 2, Group: 4},
			children: []Child{unmerged(child(1, true, "a"), 11)},
			group:    4, window: 1,
		},
		{
			name:     "then the group",
			st:       &State{Window: 1, Group: 4, Counted: []int{11}},
			children: []Child{unmerged(child(1, true, "a"), 12)},
			group:    2, window: 1,
		},
		{
			name:     "lost state: the largest completed child doubled, the window not grown below the max group",
			children: []Child{child(1, false, "a"), child(2, false, "b", "c"), child(3, true, "d")},
			group:    4, window: 2, create: []string{"e,f"},
		},
		{
			name:     "lost state at the max group: the window counts children done at it",
			children: []Child{child(1, false, "a", "b", "c", "d"), child(2, true, "e")},
			group:    4, window: 3, create: []string{"f"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := Decide(Input{Parent: parent, ParentTitle: "P", Spec: spec, State: tc.st, Children: tc.children, TriggerLabel: "overseer", StopLabel: "overseer/stop"})
			var create []string
			for _, c := range p.Create {
				create = append(create, strings.Join(c.Keys, ","))
			}
			if p.State.Group != tc.group || p.State.Window != tc.window || !slices.Equal(create, tc.create) {
				t.Errorf("group %d, window %d, create %q; want %d, %d, %q", p.State.Group, p.State.Window, create, tc.group, tc.window, tc.create)
			}
		})
	}
}
