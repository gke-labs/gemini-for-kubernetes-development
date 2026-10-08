package fanout

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

const parent = 100

func testSpec(t *testing.T, fanout string) Spec {
	t.Helper()
	md := "## Task\nDo {{.item.name}}.\n\n## Items\n- [ ] a\n- [ ] b\n- [ ] c\n- [ ] d\n\n## Finally\nClean up.\n"
	if fanout != "" {
		md += "\n## Fan-out\n" + fanout + "\n"
	}
	spec, err := Parse(md)
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

// child is an item's child written from spec, as the fan-out creates it.
func child(spec Spec, n int, key string, mods ...func(*Child)) Child {
	for _, it := range spec.Items {
		if it.Key == key {
			c := Child{Number: n, Key: key, Title: spec.ChildTitle(parent, "P", it), Body: spec.ChildBody(parent, "P", it), Open: true}
			for _, m := range mods {
				m(&c)
			}
			return c
		}
	}
	panic("no item " + key)
}

func labelled(c *Child)   { c.Labelled = true }
func closed(c *Child)     { c.Open = false; c.Labelled = true }
func notPlanned(c *Child) { c.Open = false; c.NotPlanned = true }
func withPR(n int, open, merged bool) func(*Child) {
	return func(c *Child) { c.PRs = append(c.PRs, PR{Number: n, Open: open, Merged: merged}) }
}

func input(spec Spec, st *State, children ...Child) Input {
	return Input{Parent: parent, ParentTitle: "P", Spec: spec, Children: children, State: st, TriggerLabel: "overseer", StopLabel: "overseer/stop"}
}

// summary is a plan reduced to what the tests compare.
type summary struct {
	Window      int
	Create      []string // key, + "*" when labelled
	Rewrite     []int
	Label       []int
	Stop        bool
	CloseParent bool
	Checkpoints []int
	Counted     []int
}

func summarize(p Plan) summary {
	s := summary{Window: p.State.Window, Stop: p.Stop != "", CloseParent: p.CloseParent, Checkpoints: p.State.Checkpoints, Counted: p.State.Counted}
	for _, c := range p.Create {
		k := c.Key
		if c.Final {
			k = "final"
		}
		if len(c.Labels) > 0 {
			k += "*"
		}
		s.Create = append(s.Create, k)
	}
	for _, r := range p.Rewrite {
		s.Rewrite = append(s.Rewrite, r.Number)
	}
	for _, l := range p.Label {
		s.Label = append(s.Label, l.Number)
	}
	return s
}

func TestDecide(t *testing.T) {
	spec := testSpec(t, "")
	batch := testSpec(t, "create: lazy")
	noFinally := spec
	noFinally.Finally = ""
	stale := func(c *Child) { c.Body = "old task" }

	tests := []struct {
		name string
		in   Input
		want summary
	}{
		{
			name: "start: create all, label the first window",
			in:   input(spec, nil),
			want: summary{Window: 2, Create: []string{"a*", "b*", "c", "d"}},
		},
		{
			name: "start, create batch: only the window",
			in:   input(batch, nil),
			want: summary{Window: 2, Create: []string{"a*", "b*"}},
		},
		{
			name: "stopped: nothing but the state",
			in:   func() Input { in := input(spec, nil); in.Stopped = true; return in }(),
			want: summary{Window: 2},
		},
		{
			name: "a child done: the window grows and two more are labelled",
			in: input(spec, &State{Window: 2},
				child(spec, 1, "a", closed, withPR(11, false, true)), child(spec, 2, "b", labelled),
				child(spec, 3, "c"), child(spec, 4, "d")),
			want: summary{Window: 3, Label: []int{3, 4}, Counted: []int{1}},
		},
		{
			name: "checkpoint: stop, record it, label nothing",
			in: input(spec, &State{Window: 2},
				child(spec, 1, "a", closed), child(spec, 2, "b", closed),
				child(spec, 3, "c"), child(spec, 4, "d")),
			want: summary{Window: 4, Stop: true, Checkpoints: []int{2}, Counted: []int{1, 2}},
		},
		{
			name: "resumed after the checkpoint: stale children rewritten, then labelled",
			in: input(spec, &State{Window: 4, Counted: []int{1, 2}, Checkpoints: []int{2}},
				child(spec, 1, "a", closed), child(spec, 2, "b", closed),
				child(spec, 3, "c", stale), child(spec, 4, "d")),
			want: summary{Window: 4, Rewrite: []int{3}, Label: []int{3, 4}, Checkpoints: []int{2}, Counted: []int{1, 2}},
		},
		{
			name: "labelled children are not rewritten",
			in: input(spec, &State{Window: 2},
				child(spec, 1, "a", labelled, stale), child(spec, 2, "b", labelled), child(spec, 3, "c", stale)),
			want: summary{Window: 2, Rewrite: []int{3}, Create: []string{"d"}},
		},
		{
			name: "a PR closed unmerged halves the window",
			in: input(spec, &State{Window: 5, Counted: []int{1, 2}, Checkpoints: []int{2}},
				child(spec, 1, "a", closed), child(spec, 2, "b", closed),
				child(spec, 3, "c", labelled, withPR(31, false, false)), child(spec, 4, "d", labelled)),
			want: summary{Window: 2, Checkpoints: []int{2}, Counted: []int{1, 2, 31}},
		},
		{
			name: "the window never drops below one",
			in: input(spec, &State{Window: 1},
				child(spec, 1, "a", labelled, withPR(11, false, false))),
			want: summary{Window: 1, Counted: []int{11}, Create: []string{"b", "c", "d"}},
		},
		{
			name: "the window stops at max",
			in: input(testSpec(t, "window: {start: 2, max: 2}\ncheckpoints: []"), &State{Window: 2},
				child(spec, 1, "a", closed), child(spec, 2, "b", labelled), child(spec, 3, "c"), child(spec, 4, "d")),
			want: summary{Window: 2, Label: []int{3}, Counted: []int{1}},
		},
		{
			name: "skipped: done, but the window does not grow",
			in: input(spec, &State{Window: 2},
				child(spec, 1, "a", notPlanned), child(spec, 2, "b", labelled), child(spec, 3, "c"), child(spec, 4, "d")),
			want: summary{Window: 2, Label: []int{3}, Counted: []int{1}},
		},
		{
			name: "a child unlabelled by a person is not labelled again",
			in: input(spec, &State{Window: 2, Started: []int{1, 2}},
				child(spec, 1, "a"), child(spec, 2, "b", labelled), child(spec, 3, "c"), child(spec, 4, "d")),
			want: summary{Window: 2, Label: []int{3}},
		},
		{
			name: "every item done: the final child, labelled",
			in: input(spec, &State{Window: 6, Counted: []int{1, 2, 3, 4}, Checkpoints: []int{2}},
				child(spec, 1, "a", closed), child(spec, 2, "b", closed), child(spec, 3, "c", closed), child(spec, 4, "d", notPlanned)),
			want: summary{Window: 6, Create: []string{"final*"}, Checkpoints: []int{2}, Counted: []int{1, 2, 3, 4}},
		},
		{
			name: "the final child closed: close the parent",
			in: input(spec, &State{Window: 6, Counted: []int{1, 2, 3, 4}, Checkpoints: []int{2}},
				child(spec, 1, "a", closed), child(spec, 2, "b", closed), child(spec, 3, "c", closed), child(spec, 4, "d", closed),
				Child{Number: 5, Final: true, Open: false, Labelled: true}),
			want: summary{Window: 6, CloseParent: true, Checkpoints: []int{2}, Counted: []int{1, 2, 3, 4}},
		},
		{
			name: "no Finally: close the parent once every item is done",
			in: input(noFinally, &State{Window: 6, Counted: []int{1, 2, 3, 4}, Checkpoints: []int{2}},
				child(spec, 1, "a", closed), child(spec, 2, "b", closed), child(spec, 3, "c", closed), child(spec, 4, "d", closed)),
			want: summary{Window: 6, CloseParent: true, Checkpoints: []int{2}, Counted: []int{1, 2, 3, 4}},
		},
		{
			name: "lost state: recomputed, passed checkpoints are not stopped at again",
			in: input(spec, nil,
				child(spec, 1, "a", closed), child(spec, 2, "b", closed), child(spec, 3, "c", labelled), child(spec, 4, "d")),
			want: summary{Window: 4, Label: []int{4}, Checkpoints: []int{2}, Counted: []int{1, 2}},
		},
		{
			name: "a child for an item removed from the spec is left alone",
			in: input(testSpec(t, ""), &State{Window: 2},
				child(spec, 1, "a", labelled), Child{Number: 9, Key: "gone", Open: true}),
			want: summary{Window: 2, Label: nil, Create: []string{"b*", "c", "d"}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := summarize(Decide(tc.in))
			if diff := cmp.Diff(tc.want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Decide() (-want +got):\n%s", diff)
			}
		})
	}
}

func TestCheckpointComment(t *testing.T) {
	spec := testSpec(t, "")
	p := Decide(input(spec, &State{Window: 2},
		child(spec, 1, "a", closed, withPR(11, false, true)), child(spec, 2, "b", notPlanned)))
	for _, want := range []string{"2 done", "#1 → PR #11", "#2 (skipped)", "remove `overseer/stop`"} {
		if !strings.Contains(p.Stop, want) {
			t.Errorf("checkpoint comment %q does not have %q", p.Stop, want)
		}
	}
}

func TestProgress(t *testing.T) {
	spec := testSpec(t, "")
	in := input(spec, nil)
	in.Stopped = true
	got := Progress(in, State{Window: 3},
		[]Child{child(spec, 1, "a", closed, withPR(11, false, true)), child(spec, 2, "b", labelled, withPR(12, true, false)), child(spec, 3, "c")})
	for _, want := range []string{
		ProgressMarker,
		"1 of 4 done · 1 in progress · window 3 (max 8) · stopped",
		"| a | #1 | #11 merged | done |",
		"| b | #2 | #12 open | in progress |",
		"| c | #3 | — | waiting |",
		"| d | — | — | not created |",
		"| *Finally* | — | — | after every item |",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("progress does not have %q:\n%s", want, got)
		}
	}
}
