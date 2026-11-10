package termd

import (
	"testing"
	"time"
)

// What the session reports from what its terminal heard, most exact
// signal first.
func TestActivityState(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	for _, tc := range []struct {
		name    string
		setup   func(a *activity, sig signals)
		exited  bool
		want    string
		message string
	}{
		{"nothing heard", func(*activity, signals) {}, false, StateIdle, ""},
		{"recent output", func(a *activity, _ signals) { a.output(now.Add(-time.Second)) }, false, StateWorking, ""},
		{"old output", func(a *activity, _ signals) { a.output(now.Add(-time.Minute)) }, false, StateIdle, ""},
		{"progress up", func(_ *activity, sig signals) { sig.progress(true) }, false, StateWorking, ""},
		{"progress removed", func(_ *activity, sig signals) { sig.progress(true); sig.progress(false) }, false, StateIdle, ""},
		{"bell", func(_ *activity, sig signals) { sig.attention("bell") }, false, StateNeedsYou, "bell"},
		{"bell then typing", func(a *activity, sig signals) {
			sig.attention("bell")
			a.input(time.Now().Add(time.Second))
		}, false, StateIdle, ""},
		{"blocked on permission beats output", func(a *activity, sig signals) {
			a.output(now)
			sig.status(&Status{State: "blocked", Kind: "permission", Message: "Apply?"})
		}, false, StateNeedsYou, "Apply?"},
		{"working", func(_ *activity, sig signals) { sig.status(&Status{State: "working", Message: "Editing"}) }, false, StateWorking, "Editing"},
		{"done", func(_ *activity, sig signals) { sig.status(&Status{State: "done"}) }, false, StateDone, ""},
		{"cleared falls back", func(_ *activity, sig signals) {
			sig.status(&Status{State: "error"})
			sig.status(nil)
		}, false, StateIdle, ""},
		{"exited beats everything", func(_ *activity, sig signals) { sig.status(&Status{State: "working"}) }, true, StateExited, ""},
	} {
		a := &activity{}
		tc.setup(a, a.signals())
		got, msg, _ := a.state(now, tc.exited)
		if got != tc.want || msg != tc.message {
			t.Errorf("%s: state = %q, %q; want %q, %q", tc.name, got, msg, tc.want, tc.message)
		}
	}
}
