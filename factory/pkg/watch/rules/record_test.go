package rules

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRecorderAppendsAndRollsOver(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "decisions.jsonl")
	r := NewRecorder(path, 400, nil)
	for i := range 5 {
		rc := r.Start("issues", testEnv, Item{Kind: OnIssue, Number: i})
		rc.Decided("fix", OutcomeRun)
		rc.Finish()
		rc.Finish() // once
	}
	cur, _ := os.ReadFile(path)
	old, _ := os.ReadFile(path + ".1")
	lines := strings.Count(string(cur), "\n") + strings.Count(string(old), "\n")
	if len(old) == 0 || lines < 2 || lines > 5 {
		t.Errorf("current %d bytes, rolled %d bytes, %d lines", len(cur), len(old), lines)
	}
	var rec Record
	first, _, _ := strings.Cut(string(cur), "\n")
	if err := json.Unmarshal([]byte(first), &rec); err != nil || rec.Decided[0] != "fix run" {
		t.Errorf("record %q: %v", first, err)
	}

	// Nil records nothing, and does not panic.
	var none *Recorder
	rc := none.Start("prs", testEnv, Item{})
	rc.Decided("ci", OutcomeRun)
	rc.Gated("x")
	rc.Fill(func(*Item) {})
	rc.Finish()
	if NewRecorder("", 0, nil) != nil {
		t.Error("a recorder with no file")
	}
}

func TestPastRuns(t *testing.T) {
	t0 := now.Add(-3 * time.Hour)
	starts := []time.Time{t0, t0.Add(time.Hour), t0.Add(2 * time.Hour)}
	runs := PastRuns("ci", starts, starts[2], starts[2].Add(10*time.Minute), true, "h2")
	if len(runs) != 3 || !runs[2].Failed || runs[2].Head != "h2" || !runs[2].EndedAt.Equal(starts[2].Add(10*time.Minute)) {
		t.Errorf("runs = %+v", runs)
	}
	// The last run newer than the journal's: added.
	if runs := PastRuns("ci", starts[:2], starts[2], starts[2].Add(time.Minute), false, ""); len(runs) != 3 {
		t.Errorf("runs = %+v", runs)
	}
	// No journal: the last run alone; nothing at all: none.
	if runs := PastRuns("fix", nil, t0, t0.Add(time.Minute), false, ""); len(runs) != 1 {
		t.Errorf("runs = %+v", runs)
	}
	if runs := PastRuns("fix", nil, time.Time{}, time.Time{}, false, ""); len(runs) != 0 {
		t.Errorf("runs = %+v", runs)
	}
}

func TestReplayListsDifferences(t *testing.T) {
	line := func(rec Record) string {
		b, _ := json.Marshal(rec)
		return string(b)
	}
	labelled := Item{Kind: OnIssue, Number: 1, Labels: []string{"overseer"}, UpdatedAt: now}
	in := strings.Join([]string{
		line(Record{At: now, Env: testEnv, Item: labelled, Decided: []string{"fix run"}}),
		line(Record{At: now, Env: testEnv, Item: labelled}),
		line(Record{At: now, Env: testEnv, Item: labelled, Gate: "its sandbox is busy"}),
		"",
	}, "\n")
	set, _ := Builtin()
	res, err := set.Replay(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if res.Compared != 2 || res.Gated != 1 || len(res.Diffs) != 1 || res.ByRule["fix"] != 1 {
		t.Fatalf("replay = %+v", res)
	}
	if d := res.Diffs[0]; strings.Join(d.Engine, ",") != "fix run" || len(d.Scanner) != 0 {
		t.Errorf("diff = %+v", d)
	}
	if _, err := set.Replay(strings.NewReader("{not json")); err == nil {
		t.Error("a bad line replayed")
	}
}

// A record the same as the item's last one but for its time is left out;
// one whose facts or decision changed is written.
func TestRecorderLeavesOutRepeats(t *testing.T) {
	path := filepath.Join(t.TempDir(), "decisions.jsonl")
	r := NewRecorder(path, 0, nil)
	write := func(at time.Time, mod func(*Item), decided ...string) {
		env := testEnv
		env.Now = at
		it := Item{Kind: OnPR, Number: 5, Checks: "green"}
		if mod != nil {
			mod(&it)
		}
		rc := r.Start("prs", env, it)
		for _, d := range decided {
			rule, outcome, _ := strings.Cut(d, " ")
			rc.Decided(rule, outcome)
		}
		rc.Finish()
	}
	write(now, nil)
	write(now.Add(2*time.Minute), nil)                                                // the same: left out
	write(now.Add(4*time.Minute), func(it *Item) { it.Checks = "failing" })           // facts changed
	write(now.Add(6*time.Minute), func(it *Item) { it.Checks = "failing" }, "ci run") // decision changed
	write(now.Add(8*time.Minute), func(it *Item) { it.Checks = "failing" }, "ci run") // the same: left out
	data, _ := os.ReadFile(path)
	if n := strings.Count(string(data), "\n"); n != 3 {
		t.Errorf("%d records written, want 3:\n%s", n, data)
	}
}
