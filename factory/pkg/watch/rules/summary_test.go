package rules

import (
	"testing"
	"time"
)

// The summary tallies each rule's runs and give-ups from what the scanners
// recorded, the drops at start reported in, and checks every record
// against the rules: a record the engine decides otherwise on again is an
// inconsistency; a gated one is counted, not checked.
func TestSummaryTallies(t *testing.T) {
	set, _ := Builtin()
	s := NewSummary(set)
	labelled := Item{Kind: OnIssue, Number: 1, Labels: []string{"overseer"}, UpdatedAt: now}
	s.Observe(Record{At: now, Env: testEnv, Item: labelled, Decided: []string{"fix run"}}) // consistent
	s.Observe(Record{At: now, Env: testEnv, Item: labelled})                               // inconsistent: the rules fix
	s.Observe(Record{At: now, Env: testEnv, Item: labelled, Gate: "its sandbox is busy"})  // gated
	stopped := labelled
	stopped.Labels = []string{"overseer", "overseer/stop"}
	s.Observe(Record{At: now, Env: testEnv, Item: stopped}) // consistent: nothing
	s.Dropped("ci", "head moved aaaaaaa→bbbbbbb")
	s.Dropped("ci", "the rules no longer decide ci: comments took the item")

	v := s.View()
	if v.Records != 3 || v.Consistent != 2 || v.Gated != 1 {
		t.Errorf("view = %+v", v)
	}
	if r := v.ByRule["fix"]; r.Run != 1 || r.GiveUp != 0 {
		t.Errorf("fix = %+v", r)
	}
	if r := v.ByRule["ci"]; r.Dropped != 2 || r.LastDrop != "the rules no longer decide ci: comments took the item" {
		t.Errorf("ci = %+v", r)
	}
	if len(v.Recent) != 1 || v.Recent[0].Number != 1 || len(v.Recent[0].Rules) != 1 || len(v.Recent[0].Scanner) != 0 {
		t.Errorf("recent = %+v", v.Recent)
	}

	gaveUp := Item{Kind: OnIssue, Number: 2, Labels: []string{"overseer"}, UpdatedAt: now}
	s.Observe(Record{At: now, Env: testEnv, Item: gaveUp, Decided: []string{"fix give-up"}})
	if r := s.View().ByRule["fix"]; r.GiveUp != 1 {
		t.Errorf("fix give-ups = %+v", r)
	}

	for i := range maxInconsistencies + 10 {
		it := labelled
		it.Number = 100 + i
		s.Observe(Record{At: now.Add(time.Duration(i) * time.Second), Env: testEnv, Item: it})
	}
	if v := s.View(); len(v.Recent) != maxInconsistencies || v.Recent[0].Number != 100+maxInconsistencies+9 {
		t.Errorf("recent kept %d, newest #%d", len(v.Recent), v.Recent[0].Number)
	}
	var none *Summary
	none.Observe(Record{}) // nil is a no-op
	none.Dropped("ci", "x")
}

// A recorder with a summary and no file tallies every record and writes
// nothing; repeats are left out.
func TestRecorderFeedsTheSummary(t *testing.T) {
	set, _ := Builtin()
	s := NewSummary(set)
	r := NewRecorder("", 0, s)
	for range 3 {
		rc := r.Start("issues", testEnv, Item{Kind: OnIssue, Number: 7, Labels: []string{"overseer"}, UpdatedAt: now})
		rc.Decided("fix", OutcomeRun)
		rc.Finish()
	}
	if v := s.View(); v.Records != 1 || v.Consistent != 1 || v.ByRule["fix"].Run != 1 {
		t.Errorf("view = %+v", v)
	}
}
