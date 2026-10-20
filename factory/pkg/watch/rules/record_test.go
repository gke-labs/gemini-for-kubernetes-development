package rules

import (
	"encoding/json"
	"errors"
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
	rolled, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "decisions-*.jsonl"))
	lines := strings.Count(string(cur), "\n")
	for _, f := range rolled {
		b, _ := os.ReadFile(f)
		lines += strings.Count(string(b), "\n")
	}
	if len(rolled) == 0 || lines != 5 {
		t.Errorf("current %d bytes, %d rolled files, %d lines; want the five, some rolled aside", len(cur), len(rolled), lines)
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

// The record is a file a day: a new UTC day moves the file aside as the
// day it was written, as does growing past the size; days past the
// retention go; and a directory names every day's file, in order.
func TestRecordRotatesDaily(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "decisions.jsonl")
	clock := time.Date(2026, 10, 20, 23, 0, 0, 0, time.UTC)
	r := NewRecorder(path, 0, nil)
	r.now = func() time.Time { return clock }
	write := func(n int) {
		rc := r.Start("prs", testEnv, Item{Kind: OnPR, Number: n})
		rc.Finish()
		// The file's write time is what says its day.
		_ = os.Chtimes(path, clock, clock)
	}
	write(1)
	clock = clock.Add(2 * time.Hour) // 2026-10-21
	write(2)
	if _, err := os.Stat(filepath.Join(dir, "decisions-2026-10-20.jsonl")); err != nil {
		t.Fatalf("no file for 2026-10-20: %v", err)
	}
	// An old day, past the retention, goes at the next rotation.
	old := filepath.Join(dir, "decisions-2026-08-01.jsonl")
	if err := os.WriteFile(old, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(24 * time.Hour) // 2026-10-22
	write(3)
	if _, err := os.Stat(old); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a day past the retention is kept: %v", err)
	}
	files, err := RecordFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range files {
		names = append(names, filepath.Base(f))
	}
	if got, want := strings.Join(names, " "), "decisions-2026-10-20.jsonl decisions-2026-10-21.jsonl decisions.jsonl"; got != want {
		t.Errorf("files = %s, want %s", got, want)
	}
	// Past the size, a day's file is moved aside early.
	small := NewRecorder(filepath.Join(t.TempDir(), "decisions.jsonl"), 10, nil)
	small.now = func() time.Time { return clock }
	for n := range 3 {
		rc := small.Start("prs", testEnv, Item{Kind: OnPR, Number: 100 + n})
		rc.Finish()
	}
	if files, _ := filepath.Glob(filepath.Join(filepath.Dir(small.path), "decisions-*.jsonl")); len(files) != 2 {
		t.Errorf("size rotation made %v", files)
	}
}
