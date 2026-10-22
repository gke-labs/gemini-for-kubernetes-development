package rules

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"errors"
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
// <file>.1 at maxBytes. A record the same as the item's last one but for
// its time - the same facts, decided the same - is left out unless
// RecordRepeatEvery has passed: the scanners look at every item every
// pass, and on KCC that is some 260 records a minute, most of them the
// last pass's again. A nil Recorder records nothing.
type Recorder struct {
	path     string
	maxBytes int64
	// summary, when set, tallies every record (Summary.Observe).
	summary *Summary
	// sink, when set, is handed every record (NewSinkRecorder).
	sink func(Record)
	// now, when set, is the clock rotation reads (tests).
	now func() time.Time

	mu sync.Mutex
	// last is each item's last record written: its fingerprint and when.
	last map[string]lastRecord
}

type lastRecord struct {
	sum [sha256.Size]byte
	at  time.Time
}

// RecordRepeatEvery is how often a record the same as the item's last one
// is written anyway: what keeps a decision that changes with time alone
// (a cooldown running out) in the record.
const RecordRepeatEvery = 30 * time.Minute

// How long the record is kept (a day's file each, decisions-<date>.jsonl):
// DefaultRecordBytes is where a day's file is rotated early,
// RecordKeepDays how many days are kept, and RecordKeepBytes the most all
// of them may take, the oldest going first.
const (
	DefaultRecordBytes = 1 << 30
	RecordKeepDays     = 30
	RecordKeepBytes    = 5 << 30
)

// NewRecorder records to path, and hands each record to summary; with
// neither (path "" and summary nil) it is nil and records nothing.
func NewRecorder(path string, maxBytes int64, summary *Summary) *Recorder {
	if path == "" && summary == nil {
		return nil
	}
	if maxBytes <= 0 {
		maxBytes = DefaultRecordBytes
	}
	return &Recorder{path: path, maxBytes: maxBytes, summary: summary, last: map[string]lastRecord{}}
}

// NewSinkRecorder hands every record to sink, and writes none: what a
// one-off look at an item (factory watch rules explain) records.
func NewSinkRecorder(sink func(Record)) *Recorder {
	return &Recorder{sink: sink, last: map[string]lastRecord{}}
}

// repeat reports whether rec is the same as its item's last record, but
// for its time, and that was written less than RecordRepeatEvery ago; if
// not, rec becomes the item's last record. Called with r.mu held.
func (r *Recorder) repeat(rec Record) bool {
	key := fmt.Sprintf("%s/%s/%d", rec.Scanner, rec.Item.Kind, rec.Item.Number)
	rec.At, rec.Env.Now = time.Time{}, time.Time{}
	b, err := json.Marshal(rec)
	if err != nil {
		return false
	}
	sum := sha256.Sum256(b)
	if prev, ok := r.last[key]; ok && prev.sum == sum && rec.Item.Number != 0 && time.Since(prev.at) < RecordRepeatEvery {
		return true
	}
	r.last[key] = lastRecord{sum: sum, at: time.Now()}
	return false
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
	if r.repeat(rec) {
		return
	}
	r.summary.Observe(rec)
	if r.sink != nil {
		r.sink(rec)
	}
	if r.path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o755); err != nil {
		klog.Warningf("rules record: %v", err)
		return
	}
	r.rotate(int64(len(line)))
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

// PastRuns is a rule's history of an item from what the queue keeps: its
// task's runs that ended, each with when it started and whether it failed,
// and its last run's (ListProcessedTasks) end, outcome and head, which the
// last start is given. Without ended runs, the last run alone.
func PastRuns(rule string, ended []PastRun, lastStart, lastEnd time.Time, lastFailed bool, lastHead string) []PastRun {
	out := make([]PastRun, 0, len(ended)+1)
	for _, r := range ended {
		out = append(out, PastRun{Rule: rule, StartedAt: r.StartedAt, EndedAt: r.StartedAt, Failed: r.Failed})
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
		engine, scanner, same := compare(s, rec)
		if same {
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

// rotate moves the record's file aside, as the day's file
// (decisions-<date>.jsonl, the date of its last write), when a new UTC day
// has begun since it was last written or it would grow past maxBytes, and
// then drops the days past RecordKeepDays and, oldest first, beyond
// RecordKeepBytes. Called with r.mu held.
func (r *Recorder) rotate(adding int64) {
	info, err := os.Stat(r.path)
	if err != nil {
		return
	}
	now := time.Now().UTC()
	if r.now != nil {
		now = r.now().UTC()
	}
	day := info.ModTime().UTC().Format(time.DateOnly)
	if day == now.Format(time.DateOnly) && info.Size()+adding <= r.maxBytes {
		return
	}
	base := strings.TrimSuffix(r.path, filepath.Ext(r.path))
	dest := base + "-" + day + ".jsonl"
	for n := 2; ; n++ {
		if _, err := os.Stat(dest); errors.Is(err, os.ErrNotExist) {
			break
		}
		dest = fmt.Sprintf("%s-%s.%d.jsonl", base, day, n)
	}
	if err := os.Rename(r.path, dest); err != nil {
		klog.Warningf("rules record: rotating %s: %v", r.path, err)
		return
	}
	pruneRecord(base, now)
}

// pruneRecord drops a record's day files older than RecordKeepDays, then,
// oldest first, those beyond RecordKeepBytes.
func pruneRecord(base string, now time.Time) {
	files, _ := filepath.Glob(base + "-*.jsonl")
	sort.Strings(files)
	oldest := now.AddDate(0, 0, -RecordKeepDays).Format(time.DateOnly)
	var kept []string
	var total int64
	for _, f := range files {
		day := strings.TrimPrefix(f, base+"-")
		if len(day) >= len(time.DateOnly) && day[:len(time.DateOnly)] < oldest {
			_ = os.Remove(f)
			continue
		}
		if info, err := os.Stat(f); err == nil {
			total += info.Size()
		}
		kept = append(kept, f)
	}
	for len(kept) > 0 && total > RecordKeepBytes {
		if info, err := os.Stat(kept[0]); err == nil {
			total -= info.Size()
		}
		_ = os.Remove(kept[0])
		kept = kept[1:]
	}
}

// RecordFiles are the record files a path names, in the order they were
// written: a file is itself; a directory is its decisions-<date>.jsonl days,
// an older decisions.jsonl.1, and decisions.jsonl, the one being written.
func RecordFiles(path string) ([]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return []string{path}, nil
	}
	days, _ := filepath.Glob(filepath.Join(path, "decisions-*.jsonl"))
	sort.Strings(days)
	var out []string
	out = append(out, days...)
	for _, f := range []string{"decisions.jsonl.1", "decisions.jsonl"} {
		if _, err := os.Stat(filepath.Join(path, f)); err == nil {
			out = append(out, filepath.Join(path, f))
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no record files in %s", path)
	}
	return out, nil
}
