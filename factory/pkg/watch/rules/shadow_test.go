package rules

import (
	"os"
	"testing"
	"time"
)

// The shadow decides on every record and tallies, by rule, where it agrees
// with the scanner and where it differs; gated records are counted, not
// compared.
func TestShadowTallies(t *testing.T) {
	set, _ := Builtin()
	s := NewShadow(set)
	labelled := Item{Kind: OnIssue, Number: 1, Labels: []string{"overseer"}, UpdatedAt: now}
	s.Observe(Record{At: now, Env: testEnv, Item: labelled, Decided: []string{"fix run"}}) // agrees
	s.Observe(Record{At: now, Env: testEnv, Item: labelled})                               // differs: the rules fix
	s.Observe(Record{At: now, Env: testEnv, Item: labelled, Gate: "its sandbox is busy"})  // gated
	stopped := labelled
	stopped.Labels = []string{"overseer", "overseer/stop"}
	s.Observe(Record{At: now, Env: testEnv, Item: stopped}) // agrees: nothing

	sum := s.Summary()
	if sum.Compared != 3 || sum.Agree != 2 || sum.Gated != 1 {
		t.Errorf("summary = %+v", sum)
	}
	if r := sum.ByRule["fix"]; r.Decided != 2 || r.Differ != 1 {
		t.Errorf("fix = %+v", r)
	}
	if len(sum.Recent) != 1 || sum.Recent[0].Number != 1 || len(sum.Recent[0].Rules) != 1 || len(sum.Recent[0].Scanner) != 0 {
		t.Errorf("recent = %+v", sum.Recent)
	}

	for i := range maxShadowDiffs + 10 {
		it := labelled
		it.Number = 100 + i
		s.Observe(Record{At: now.Add(time.Duration(i) * time.Second), Env: testEnv, Item: it})
	}
	if sum := s.Summary(); len(sum.Recent) != maxShadowDiffs || sum.Recent[0].Number != 100+maxShadowDiffs+9 {
		t.Errorf("recent kept %d, newest #%d", len(sum.Recent), sum.Recent[0].Number)
	}
	var none *Shadow
	none.Observe(Record{}) // nil is a no-op
}

// A recorder with a shadow and no file compares every record and writes
// nothing; repeats are left out of both.
func TestRecorderFeedsTheShadow(t *testing.T) {
	set, _ := Builtin()
	s := NewShadow(set)
	dir := t.TempDir()
	r := NewRecorder("", 0, s)
	for range 3 {
		rc := r.Start("issues", testEnv, Item{Kind: OnIssue, Number: 7, Labels: []string{"overseer"}, UpdatedAt: now})
		rc.Decided("fix", OutcomeRun)
		rc.Finish()
	}
	if sum := s.Summary(); sum.Compared != 1 || sum.Agree != 1 {
		t.Errorf("summary = %+v, want one record compared (the repeats left out)", sum)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("wrote %v", entries)
	}
}
