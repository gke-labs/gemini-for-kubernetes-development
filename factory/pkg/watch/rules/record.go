package rules

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"k8s.io/klog/v2"
)

// Record is one decision today's scanners made about an item, with the
// facts they made it on: what the replay runs Decide on and compares
// (design: Moving without regressing, replay tests).
type Record struct {
	At      time.Time `json:"at"`
	Scanner string    `json:"scanner"`
	Env     Env       `json:"env"`
	Item    Item      `json:"item"`
	// Decided is what the scanner did, as "<rule> <outcome>" (fix run, ci
	// give-up, ready run), in the rules' names: nothing when it did
	// nothing.
	Decided []string `json:"decided,omitempty"`
	// Gate is a reason outside the rules the scanner held the item back
	// for (its sandbox busy, or needing a person): the replay does not
	// compare it.
	Gate string `json:"gate,omitempty"`
}

// Recorder appends Records to a JSON-lines file, rolling it over to
// <file>.1 at maxBytes. A nil Recorder records nothing.
type Recorder struct {
	path     string
	maxBytes int64

	mu sync.Mutex
}

// DefaultRecordBytes is where a record file rolls over.
const DefaultRecordBytes = 32 << 20

// NewRecorder records to path, or is nil when path is "".
func NewRecorder(path string, maxBytes int64) *Recorder {
	if path == "" {
		return nil
	}
	if maxBytes <= 0 {
		maxBytes = DefaultRecordBytes
	}
	return &Recorder{path: path, maxBytes: maxBytes}
}

// Write appends one record. Recording is best effort: an error is logged,
// never returned to the scanner it would hold up.
func (r *Recorder) Write(rec Record) {
	if r == nil {
		return
	}
	line, err := json.Marshal(rec)
	if err != nil {
		klog.Warningf("rules record: %v", err)
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(r.path), 0o755); err != nil {
		klog.Warningf("rules record: %v", err)
		return
	}
	if info, err := os.Stat(r.path); err == nil && info.Size()+int64(len(line)) > r.maxBytes {
		_ = os.Rename(r.path, r.path+".1")
	}
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		klog.Warningf("rules record: %v", err)
		return
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		klog.Warningf("rules record: %v", err)
	}
}

// Recording is one item's record being made while a scanner decides about
// it: the scanner fills in Item as it learns the facts, notes what it does
// (Decided) or what outside the rules held it (Gated), and Finish writes
// it. Every method of a nil Recording does nothing, so a scanner calls them
// whether or not it records.
type Recording struct {
	Item *Item

	recorder *Recorder
	rec      Record
	done     bool
}

// Start begins a record of an item.
func (r *Recorder) Start(scanner string, env Env, item Item) *Recording {
	if r == nil {
		return nil
	}
	rc := &Recording{recorder: r, rec: Record{At: env.Now, Scanner: scanner, Env: env, Item: item}}
	rc.Item = &rc.rec.Item
	return rc
}

// Decided notes what the scanner did, in a rule's name and outcome.
func (rc *Recording) Decided(rule, outcome string) {
	if rc == nil {
		return
	}
	rc.rec.Decided = append(rc.rec.Decided, rule+" "+outcome)
}

// Gated notes a reason outside the rules the scanner held the item back.
func (rc *Recording) Gated(why string) {
	if rc == nil || rc.rec.Gate != "" {
		return
	}
	rc.rec.Gate = why
}

// Finish writes the record, once.
func (rc *Recording) Finish() {
	if rc == nil || rc.done {
		return
	}
	rc.done = true
	rc.recorder.Write(rc.rec)
}

// Fill sets fields of the item being recorded.
func (rc *Recording) Fill(f func(*Item)) {
	if rc == nil {
		return
	}
	f(rc.Item)
}

// PastRuns is a rule's history of an item from what the queue keeps: the
// start times of its task's runs that ended, and its last run's
// (ListProcessedTasks) end, outcome and head, which the last start is
// given. Without starts, the last run alone.
func PastRuns(rule string, starts []time.Time, lastStart, lastEnd time.Time, lastFailed bool, lastHead string) []PastRun {
	out := make([]PastRun, 0, len(starts)+1)
	for _, st := range starts {
		out = append(out, PastRun{Rule: rule, StartedAt: st, EndedAt: st})
	}
	if lastEnd.IsZero() {
		return out
	}
	last := PastRun{Rule: rule, StartedAt: lastStart, EndedAt: lastEnd, Failed: lastFailed, Head: lastHead}
	for i := len(out) - 1; i >= 0; i-- {
		if out[i].StartedAt.Equal(lastStart) {
			out[i] = last
			return out
		}
	}
	if n := len(out); n == 0 || lastStart.After(out[n-1].StartedAt) {
		out = append(out, last)
	}
	return out
}

// ReplayResult is how a record set replays: the records compared, and those
// whose decisions differ from the scanners'.
type ReplayResult struct {
	Compared int
	Gated    int
	Diffs    []ReplayDiff
	// ByRule counts, per rule, the records where the rule was decided by
	// one side and not the other.
	ByRule map[string]int
}

// ReplayDiff is one record whose decisions differ.
type ReplayDiff struct {
	Record  Record
	Engine  []string
	Scanner []string
}

// Replay runs the set on every record read from in and compares its
// decisions with the scanner's. A gated record is counted, not compared.
func (s Set) Replay(in io.Reader) (ReplayResult, error) {
	res := ReplayResult{ByRule: map[string]int{}}
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	line := 0
	for sc.Scan() {
		line++
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		var rec Record
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			return res, fmt.Errorf("line %d: %w", line, err)
		}
		if rec.Gate != "" {
			res.Gated++
			continue
		}
		res.Compared++
		env := rec.Env
		if env.Now.IsZero() {
			env.Now = rec.At
		}
		var engine []string
		for _, d := range s.Decide(rec.Item, env) {
			engine = append(engine, d.Rule+" "+d.Outcome)
		}
		scanner := append([]string(nil), rec.Decided...)
		sort.Strings(engine)
		sort.Strings(scanner)
		if strings.Join(engine, ",") == strings.Join(scanner, ",") {
			continue
		}
		res.Diffs = append(res.Diffs, ReplayDiff{Record: rec, Engine: engine, Scanner: scanner})
		for _, rule := range symmetricRules(engine, scanner) {
			res.ByRule[rule]++
		}
	}
	return res, sc.Err()
}

// symmetricRules are the rules decided on one side and not the other.
func symmetricRules(a, b []string) []string {
	count := map[string]int{}
	for _, d := range a {
		count[d]++
	}
	for _, d := range b {
		count[d]--
	}
	seen := map[string]bool{}
	var out []string
	for d, n := range count {
		rule, _, _ := strings.Cut(d, " ")
		if n != 0 && !seen[rule] {
			seen[rule] = true
			out = append(out, rule)
		}
	}
	sort.Strings(out)
	return out
}
