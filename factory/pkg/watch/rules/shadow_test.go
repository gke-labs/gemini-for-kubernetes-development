package rules

import (
	"encoding/json"
	"os"
	"strings"
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

// Once the engine decides a rule for the scanner, Decided is the engine's
// and agrees by construction: the shadow compares the engine with the
// scanner's own condition instead, per rule.
func TestShadowHanded(t *testing.T) {
	set, _ := Builtin()
	s := NewShadow(set)
	ci := Item{Kind: OnPR, Number: 9, Labels: []string{"overseer"}, Assignees: []string{"bot"}, Author: "bot", Checks: "failing", Head: "h", UpdatedAt: now}
	env := testEnv
	env.Pool = []string{"bot"}
	s.Observe(Record{At: now, Env: env, Item: ci, Decided: []string{"ci run"}, Handed: []Handed{{Rule: "ci", Legacy: true}}})  // the scanner would have too
	s.Observe(Record{At: now, Env: env, Item: ci, Decided: []string{"ci run"}, Handed: []Handed{{Rule: "ci", Legacy: false}}}) // it would not
	sum := s.Summary()
	if sum.Compared != 2 || sum.Agree != 2 {
		t.Errorf("summary = %+v: Decided is the engine's, so it agrees", sum)
	}
	if h := sum.ByRule["ci"].Handed; h == nil || h.Decided != 2 || h.Differ != 1 {
		t.Errorf("ci handed = %+v, want 2 decided, 1 differing", h)
	}

	b, _ := json.Marshal(Record{At: now, Env: env, Item: ci, Decided: []string{"ci run"}, Handed: []Handed{{Rule: "ci", Legacy: false}}})
	res, err := set.Replay(strings.NewReader(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	if h := res.Handed["ci"]; h.Decided != 1 || h.Differ != 1 || len(res.Diffs) != 0 {
		t.Errorf("replay = %+v", res)
	}
}
