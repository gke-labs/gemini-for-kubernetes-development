/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package tokenusage

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Store persists usage records and subjects as JSONL append logs and keeps
// full in-memory indexes, rebuilt by replaying the logs at startup. On
// replay the last line for a key wins, so upserts are plain appends.
type Store struct {
	mu          sync.Mutex
	root        string
	records     map[string]UsageRecord
	subjects    map[string]Subject
	tasks       map[string]TaskRecord
	outcomes    map[string]OutcomeRecord
	summarized  map[string]bool // workflow session -> summary comment posted
	log         *os.File
	subjectsLog *os.File
	tasksLog    *os.File
	outcomesLog *os.File

	// The archive tier (archive.go); nil keeps everything on the PVC.
	archive         Archive
	flushMu         sync.Mutex
	nextSeq         int64          // the number the next sealed segment gets
	segsSinceSnap   map[string]int // per log: archived segments above its latest snapshot
	summarizedDirty bool           // summarized changed since it was last archived
}

// NewStore opens the store at root with no archive.
func NewStore(root string) (*Store, error) {
	return OpenStore(context.Background(), root, nil)
}

// OpenStore opens the store at root, replaying archive (when not nil)
// before what the PVC holds; Flush moves the PVC's logs to the archive.
func OpenStore(ctx context.Context, root string, archive Archive) (*Store, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("creating storage root: %w", err)
	}
	// A task or feedback PUT that died half-way left its temporary directory.
	_ = os.RemoveAll(filepath.Join(root, "tasks.tmp"))
	_ = os.RemoveAll(filepath.Join(root, "feedback.tmp"))
	s := &Store{
		root:          root,
		records:       map[string]UsageRecord{},
		subjects:      map[string]Subject{},
		tasks:         map[string]TaskRecord{},
		outcomes:      map[string]OutcomeRecord{},
		summarized:    map[string]bool{},
		archive:       archive,
		nextSeq:       1,
		segsSinceSnap: map[string]int{},
	}
	if err := s.loadLog(ctx, logRecords, func(r io.Reader) error {
		return replayLog(r, func(rec UsageRecord) string { return rec.Key }, s.records)
	}); err != nil {
		return nil, err
	}
	if err := s.loadLog(ctx, logSubjects, func(r io.Reader) error {
		return replayLog(r, func(sub Subject) string { return sub.Key }, s.subjects)
	}); err != nil {
		return nil, err
	}
	if err := s.loadLog(ctx, logTasks, func(r io.Reader) error {
		return replayLog(r, func(rec TaskRecord) string { return rec.Key }, s.tasks)
	}); err != nil {
		return nil, err
	}
	if err := s.loadLog(ctx, logOutcomes, func(r io.Reader) error {
		return replayLog(r, func(rec OutcomeRecord) string { return rec.Key }, s.outcomes)
	}); err != nil {
		return nil, err
	}
	if err := s.loadSummarized(); err != nil {
		return nil, err
	}
	if err := s.loadArchivedSummarized(ctx); err != nil {
		return nil, err
	}
	// The PVC's set may never have been archived (the PVC predates the
	// archive); the first flush uploads it.
	s.summarizedDirty = archive != nil && len(s.summarized) > 0
	log, err := os.OpenFile(s.recordsPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("opening records log: %w", err)
	}
	s.log = log
	subjectsLog, err := os.OpenFile(s.subjectsPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		_ = log.Close()
		return nil, fmt.Errorf("opening subjects log: %w", err)
	}
	s.subjectsLog = subjectsLog
	tasksLog, err := os.OpenFile(s.activePath(logTasks), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		_ = log.Close()
		_ = subjectsLog.Close()
		return nil, fmt.Errorf("opening task index log: %w", err)
	}
	s.tasksLog = tasksLog
	outcomesLog, err := os.OpenFile(s.activePath(logOutcomes), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		_ = log.Close()
		_ = subjectsLog.Close()
		_ = tasksLog.Close()
		return nil, fmt.Errorf("opening outcomes log: %w", err)
	}
	s.outcomesLog = outcomesLog
	return s, nil
}

func (s *Store) recordsPath() string    { return s.activePath(logRecords) }
func (s *Store) subjectsPath() string   { return s.activePath(logSubjects) }
func (s *Store) summarizedPath() string { return filepath.Join(s.root, "summarized.json") }

// replayLog adds a JSONL append log to an in-memory index; the last line
// for a key wins. Corrupt lines (e.g. torn write on crash) are skipped
// rather than refusing to start.
func replayLog[T any](r io.Reader, keyOf func(T) string, into map[string]T) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var v T
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			continue
		}
		if key := keyOf(v); key != "" {
			into[key] = v
		}
	}
	return scanner.Err()
}

func (s *Store) loadSummarized() error {
	data, err := os.ReadFile(s.summarizedPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return json.Unmarshal(data, &s.summarized)
}

// Put upserts a record by key. The first RecordedAt for a key is preserved
// across upserts so daily aggregation buckets stay stable (a sweep re-post
// of an already-pushed task must not move its usage to a later day).
// Identical re-posts are skipped without touching the log.
func (s *Store) Put(rec UsageRecord) error {
	if rec.Key == "" {
		return fmt.Errorf("record key is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.records[rec.Key]; ok {
		if existing.RecordedAt != "" {
			rec.RecordedAt = existing.RecordedAt
		}
		if reflect.DeepEqual(existing, rec) {
			return nil
		}
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if _, err := s.log.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("appending record: %w", err)
	}
	if err := s.log.Sync(); err != nil {
		return fmt.Errorf("syncing records log: %w", err)
	}
	s.records[rec.Key] = rec
	return nil
}

// PutSubject upserts subject metadata by key, merging with the stored
// subject: empty incoming fields never blank out known values, so a late
// "closed" update without createdAt keeps the original creation time.
func (s *Store) PutSubject(sub Subject) error {
	if sub.Key == "" {
		return fmt.Errorf("subject key is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.subjects[sub.Key]; ok {
		if sub.Repo == "" {
			sub.Repo = existing.Repo
		}
		if sub.Kind == "" {
			sub.Kind = existing.Kind
		}
		if sub.Number == 0 {
			sub.Number = existing.Number
		}
		if sub.State == "" {
			sub.State = existing.State
		}
		if sub.CreatedAt == "" {
			sub.CreatedAt = existing.CreatedAt
		}
		if sub.ClosedAt == "" {
			sub.ClosedAt = existing.ClosedAt
		}
		if sub.UpdatedAt == "" {
			sub.UpdatedAt = existing.UpdatedAt
		}
		merged := sub
		merged.UpdatedAt = existing.UpdatedAt
		if reflect.DeepEqual(existing, merged) {
			return nil // only UpdatedAt would change; skip the churn
		}
	}
	line, err := json.Marshal(sub)
	if err != nil {
		return err
	}
	if _, err := s.subjectsLog.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("appending subject: %w", err)
	}
	if err := s.subjectsLog.Sync(); err != nil {
		return fmt.Errorf("syncing subjects log: %w", err)
	}
	s.subjects[sub.Key] = sub
	return nil
}

// ListFilter selects records. Zero values mean "no filter".
type ListFilter struct {
	Repo     string
	Issue    int
	PR       int
	Workflow string
	Limit    int
}

func (f ListFilter) matches(rec UsageRecord) bool {
	if f.Repo != "" && rec.Repo != f.Repo {
		return false
	}
	if f.Issue != 0 && !recordTouchesIssue(rec, f.Issue) {
		return false
	}
	if f.PR != 0 && rec.PR != f.PR {
		return false
	}
	if f.Workflow != "" && rec.Workflow != f.Workflow {
		return false
	}
	return true
}

func recordTouchesIssue(rec UsageRecord, issue int) bool {
	if rec.Issue == issue {
		return true
	}
	for _, n := range rec.Issues {
		if n == issue {
			return true
		}
	}
	return false
}

// List returns matching records, newest first (by RecordedAt, then key).
func (s *Store) List(f ListFilter) []UsageRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []UsageRecord
	for _, rec := range s.records {
		if f.matches(rec) {
			out = append(out, rec)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].RecordedAt != out[j].RecordedAt {
			return out[i].RecordedAt > out[j].RecordedAt
		}
		return out[i].Key > out[j].Key
	})
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out
}

// The three rollup views are mutually exclusive so each record is counted in
// exactly one of them:
//   - workflow rollups take every record tagged with a workflow session or
//     referencing a workflow issue;
//   - issue rollups take the remaining records linked to an issue (the issue
//     sandbox and any PR work it led to — an "issue/PR sandbox");
//   - PR rollups take what is left: standalone PR work (reviews,
//     investigations, adoptions) not tied to any issue.

// RollupByIssue groups non-workflow records by issue number: a record counts
// toward issue N if rec.Issue == N or N appears in rec.Issues (PR tasks
// linked back to the issue). Issues owned by a workflow session are excluded
// (they appear in the workflow rollups instead).
func (s *Store) RollupByIssue(repo string) []Rollup {
	wfIssues := s.workflowIssueSet(repo)
	return s.rollup(repo, "issue", func(rec UsageRecord) []string {
		if recordInWorkflow(rec, wfIssues) {
			return nil
		}
		seen := map[int]bool{}
		var keys []string
		if rec.Issue != 0 {
			seen[rec.Issue] = true
			keys = append(keys, strconv.Itoa(rec.Issue))
		}
		for _, n := range rec.Issues {
			if n != 0 && !seen[n] {
				seen[n] = true
				keys = append(keys, strconv.Itoa(n))
			}
		}
		return keys
	})
}

// RollupByPR groups standalone PR records by PR number. Records linked to a
// workflow or to an issue are excluded (they appear in the workflow/issue
// rollups instead).
func (s *Store) RollupByPR(repo string) []Rollup {
	wfIssues := s.workflowIssueSet(repo)
	return s.rollup(repo, "pr", func(rec UsageRecord) []string {
		if rec.PR == 0 || recordInWorkflow(rec, wfIssues) || recordTouchesAnyIssue(rec) {
			return nil
		}
		return []string{strconv.Itoa(rec.PR)}
	})
}

// RollupByDay groups all records by the day (UTC date of RecordedAt, which
// is when the task's usage was first pushed), newest day first.
func (s *Store) RollupByDay(repo string) []Rollup {
	out := s.rollup(repo, "", func(rec UsageRecord) []string {
		if len(rec.RecordedAt) < 10 {
			return []string{"unknown"}
		}
		return []string{rec.RecordedAt[:10]}
	})
	// rollup sorts ascending; days read better newest first.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// workflowIssueSet returns the issue numbers owned by a workflow session
// (i.e. any record tagged workflow "issue-N").
func (s *Store) workflowIssueSet(repo string) map[int]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	set := map[int]bool{}
	for _, rec := range s.records {
		if repo != "" && rec.Repo != repo {
			continue
		}
		if n := workflowSessionIssue(rec.Workflow); n != 0 {
			set[n] = true
		}
	}
	return set
}

func recordInWorkflow(rec UsageRecord, wfIssues map[int]bool) bool {
	if rec.Workflow != "" {
		return true
	}
	if wfIssues[rec.Issue] {
		return true
	}
	for _, n := range rec.Issues {
		if wfIssues[n] {
			return true
		}
	}
	return false
}

func recordTouchesAnyIssue(rec UsageRecord) bool {
	if rec.Issue != 0 {
		return true
	}
	for _, n := range rec.Issues {
		if n != 0 {
			return true
		}
	}
	return false
}

// RollupByWorkflow groups records by workflow session. A session "issue-N"
// also absorbs records that reference issue N (rec.Issue or rec.Issues)
// without an explicit workflow tag, so PR tasks spawned by the workflow
// count toward it.
func (s *Store) RollupByWorkflow(repo string) []Rollup {
	s.mu.Lock()
	sessions := map[string]bool{}
	for _, rec := range s.records {
		if rec.Workflow != "" && (repo == "" || rec.Repo == repo) {
			sessions[rec.Workflow] = true
		}
	}
	s.mu.Unlock()

	var out []Rollup
	for session := range sessions {
		r := s.WorkflowRollup(repo, session, true)
		if r != nil {
			out = append(out, *r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// WorkflowRollup aggregates all records belonging to one workflow session,
// optionally including the per-task records. Returns nil if nothing matched.
func (s *Store) WorkflowRollup(repo, session string, includeRecords bool) *Rollup {
	issueNum := workflowSessionIssue(session)
	s.mu.Lock()
	defer s.mu.Unlock()

	r := Rollup{Key: session, Repo: repo}
	for _, rec := range s.records {
		if repo != "" && rec.Repo != repo {
			continue
		}
		if rec.Workflow != session && !(issueNum != 0 && recordTouchesIssue(rec, issueNum)) {
			continue
		}
		accumulate(&r, rec)
		if r.Repo == "" {
			r.Repo = rec.Repo
		}
		if rec.WorkflowName != "" {
			r.WorkflowName = rec.WorkflowName
		}
		if includeRecords {
			r.Records = append(r.Records, rec)
		}
	}
	if r.TaskCount == 0 {
		return nil
	}
	// A workflow session "issue-N" is subject issue-N.
	s.attachSubjectLocked(&r, session)
	sortRollup(&r)
	return &r
}

// attachSubjectLocked copies subject metadata onto a rollup; the caller must
// hold s.mu.
func (s *Store) attachSubjectLocked(r *Rollup, subjectKey string) {
	sub, ok := s.subjects[subjectKey]
	if !ok {
		return
	}
	r.State = sub.State
	r.CreatedAt = sub.CreatedAt
	r.ClosedAt = sub.ClosedAt
}

// workflowSessionIssue parses "issue-N" session ids; returns 0 otherwise.
func workflowSessionIssue(session string) int {
	rest, ok := strings.CutPrefix(session, "issue-")
	if !ok {
		return 0
	}
	n, err := strconv.Atoi(rest)
	if err != nil {
		return 0
	}
	return n
}

// rollup groups records by the keys keysFor yields. When subjectKind is
// non-empty, each group is joined with the subject "<subjectKind>-<key>" to
// expose state and age timestamps.
func (s *Store) rollup(repo string, subjectKind string, keysFor func(UsageRecord) []string) []Rollup {
	s.mu.Lock()
	defer s.mu.Unlock()
	groups := map[string]*Rollup{}
	for _, rec := range s.records {
		if repo != "" && rec.Repo != repo {
			continue
		}
		for _, key := range keysFor(rec) {
			g, ok := groups[key]
			if !ok {
				g = &Rollup{Key: key, Repo: rec.Repo}
				groups[key] = g
			}
			accumulate(g, rec)
			g.Records = append(g.Records, rec)
		}
	}
	out := make([]Rollup, 0, len(groups))
	for _, g := range groups {
		if subjectKind != "" {
			s.attachSubjectLocked(g, subjectKind+"-"+g.Key)
		}
		sortRollup(g)
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool {
		// Numeric keys sort numerically (issue/PR numbers).
		a, errA := strconv.Atoi(out[i].Key)
		b, errB := strconv.Atoi(out[j].Key)
		if errA == nil && errB == nil {
			return a < b
		}
		return out[i].Key < out[j].Key
	})
	return out
}

func accumulate(r *Rollup, rec UsageRecord) {
	r.TaskCount++
	MergeStats(&r.Stats, rec.Stats)
	if rec.Issue != 0 {
		r.Issues = appendUnique(r.Issues, rec.Issue)
	}
	for _, n := range rec.Issues {
		if n != 0 {
			r.Issues = appendUnique(r.Issues, n)
		}
	}
	if rec.PR != 0 {
		r.PRs = appendUnique(r.PRs, rec.PR)
	}
}

func sortRollup(r *Rollup) {
	sort.Ints(r.Issues)
	sort.Ints(r.PRs)
	sort.Slice(r.Records, func(i, j int) bool { return r.Records[i].Key < r.Records[j].Key })
}

func appendUnique(s []int, n int) []int {
	for _, v := range s {
		if v == n {
			return s
		}
	}
	return append(s, n)
}

// MarkSummarized atomically records that a workflow summary comment is being
// posted for the session. It returns true if a summary was already posted
// (the caller must not post again).
func (s *Store) MarkSummarized(session string) (alreadyPosted bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.summarized[session] {
		return true, nil
	}
	s.summarized[session] = true
	s.summarizedDirty = true
	data, err := json.MarshalIndent(s.summarized, "", "  ")
	if err != nil {
		return false, err
	}
	tmp := s.summarizedPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, s.summarizedPath()); err != nil {
		return false, err
	}
	return false, nil
}

// Close closes the append logs.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.log.Close()
	if err2 := s.subjectsLog.Close(); err == nil {
		err = err2
	}
	if err2 := s.tasksLog.Close(); err == nil {
		err = err2
	}
	if err2 := s.outcomesLog.Close(); err == nil {
		err = err2
	}
	return err
}

func parseTimeOrNow(tsStr string) time.Time {
	if tsStr != "" {
		if t, err := time.Parse(time.RFC3339, tsStr); err == nil {
			return t
		}
		if t, err := time.Parse("2006-01-02T15:04:05Z07:00", tsStr); err == nil {
			return t
		}
	}
	return time.Now().UTC()
}

// SlowestShellCommands computes top 30 aggregated shell commands and top 5 slowest calls per period (day, week, month).
func (s *Store) SlowestShellCommands(repoFilter string) SlowestCommandsResponse {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	dayLimit := now.Add(-24 * time.Hour)
	weekLimit := now.Add(-7 * 24 * time.Hour)
	monthLimit := now.Add(-30 * 24 * time.Hour)

	type aggState struct {
		count          int
		totalSec       float64
		maxSec         float64
		lastExecutedAt string
	}

	aggMap := map[string]*aggState{}
	var dayCalls []ShellCall
	var weekCalls []ShellCall
	var monthCalls []ShellCall

	for _, rec := range s.records {
		if repoFilter != "" && rec.Repo != repoFilter {
			continue
		}
		if rec.ToolTelemetry == nil || len(rec.ToolTelemetry.ShellCalls) == 0 {
			continue
		}

		for _, call := range rec.ToolTelemetry.ShellCalls {
			c := call
			if c.Repo == "" {
				c.Repo = rec.Repo
			}
			if c.Sandbox == "" {
				c.Sandbox = rec.Sandbox
			}
			if c.TaskType == "" {
				c.TaskType = rec.TaskType
			}
			if c.TaskDir == "" {
				c.TaskDir = rec.TaskDir
			}
			if c.Timestamp == "" {
				c.Timestamp = rec.RecordedAt
			}

			st, exists := aggMap[c.Cmd]
			if !exists {
				st = &aggState{}
				aggMap[c.Cmd] = st
			}
			st.count++
			st.totalSec += c.DurationSec
			if c.DurationSec > st.maxSec {
				st.maxSec = c.DurationSec
			}
			if c.Timestamp > st.lastExecutedAt {
				st.lastExecutedAt = c.Timestamp
			}

			t := parseTimeOrNow(c.Timestamp)
			if t.After(dayLimit) {
				dayCalls = append(dayCalls, c)
			}
			if t.After(weekLimit) {
				weekCalls = append(weekCalls, c)
			}
			if t.After(monthLimit) {
				monthCalls = append(monthCalls, c)
			}
		}
	}

	var topAgg []AggregatedCommand
	for cmd, st := range aggMap {
		avgSec := 0.0
		if st.count > 0 {
			avgSec = float64(int(st.totalSec/float64(st.count)*1000+0.5)) / 1000.0
		}
		topAgg = append(topAgg, AggregatedCommand{
			Cmd:            cmd,
			Count:          st.count,
			TotalSec:       float64(int(st.totalSec*1000+0.5)) / 1000.0,
			AvgSec:         avgSec,
			MaxSec:         float64(int(st.maxSec*1000+0.5)) / 1000.0,
			LastExecutedAt: st.lastExecutedAt,
		})
	}

	sort.Slice(topAgg, func(i, j int) bool {
		if topAgg[i].MaxSec != topAgg[j].MaxSec {
			return topAgg[i].MaxSec > topAgg[j].MaxSec
		}
		return topAgg[i].Count > topAgg[j].Count
	})
	if len(topAgg) > 30 {
		topAgg = topAgg[:30]
	}

	sortCalls := func(calls []ShellCall) []ShellCall {
		sort.Slice(calls, func(i, j int) bool {
			return calls[i].DurationSec > calls[j].DurationSec
		})
		if len(calls) > 5 {
			return calls[:5]
		}
		if calls == nil {
			return []ShellCall{}
		}
		return calls
	}

	resp := SlowestCommandsResponse{
		TopSlowestCommands: topAgg,
		SlowestByPeriod: PeriodSlowest{
			Day:   sortCalls(dayCalls),
			Week:  sortCalls(weekCalls),
			Month: sortCalls(monthCalls),
		},
	}
	if resp.TopSlowestCommands == nil {
		resp.TopSlowestCommands = []AggregatedCommand{}
	}
	return resp
}

func parseTime(tsStr string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, tsStr); err == nil {
		return t, nil
	}
	if t, err := time.Parse(time.RFC3339, tsStr); err == nil {
		return t, nil
	}
	if t, err := time.Parse("2006-01-02", tsStr); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("cannot parse time %q", tsStr)
}

// PutOutcome appends an outcome record to the outcomes log and updates the in-memory index.
func (s *Store) PutOutcome(rec OutcomeRecord) error {
	if rec.Key == "" {
		if rec.Repo != "" && rec.PR > 0 {
			rec.Key = OutcomeKey(rec.Repo, rec.PR)
		} else {
			return errors.New("key is required")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("marshaling outcome: %w", err)
	}
	if _, err := s.outcomesLog.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("appending to outcomes log: %w", err)
	}
	if err := s.outcomesLog.Sync(); err != nil {
		return fmt.Errorf("syncing outcomes log: %w", err)
	}
	s.outcomes[rec.Key] = rec
	return nil
}

// ListOutcomes returns matching outcome records, newest first.
func (s *Store) ListOutcomes(f OutcomeFilter) []OutcomeRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []OutcomeRecord
	for _, rec := range s.outcomes {
		if f.Repo != "" && rec.Repo != f.Repo {
			continue
		}
		if f.PR != 0 && rec.PR != f.PR {
			continue
		}
		if !f.Since.IsZero() || !f.Until.IsZero() {
			tStr := rec.ClosedAt
			if tStr == "" {
				tStr = rec.OpenedAt
			}
			if tStr != "" {
				t, err := parseTime(tStr)
				if err == nil {
					if !f.Since.IsZero() && t.Before(f.Since) {
						continue
					}
					if !f.Until.IsZero() && t.After(f.Until) {
						continue
					}
				}
			}
		}
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool {
		tI := out[i].ClosedAt
		if tI == "" {
			tI = out[i].OpenedAt
		}
		tJ := out[j].ClosedAt
		if tJ == "" {
			tJ = out[j].OpenedAt
		}
		if tI != tJ {
			return tI > tJ
		}
		return out[i].Key < out[j].Key
	})
	return out
}

func (s *Store) tokensForPR(repo string, pr int) int64 {
	var total int64
	for _, rec := range s.records {
		if rec.PR == pr && (repo == "" || rec.Repo == repo) {
			for _, m := range rec.Stats.Models {
				if m.Tokens.Total > 0 {
					total += m.Tokens.Total
				} else {
					total += m.Tokens.Input + m.Tokens.Output
				}
			}
		}
	}
	return total
}

// RollupOutcomes computes per-skill or per-workflow aggregate statistics.
func (s *Store) RollupOutcomes(repo string, since time.Time, by string) ([]OutcomeRollup, error) {
	if by != "skill" && by != "workflow" {
		return nil, fmt.Errorf("by must be 'skill' or 'workflow', got %q", by)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	type itemAgg struct {
		totalPRs     int
		mergedPRs    int
		botTotalPRs  int
		botMergedPRs int
		carePasses   int
		humanEdits   int
		totalTokens  int64
	}
	aggs := make(map[string]*itemAgg)

	for _, rec := range s.outcomes {
		if repo != "" && rec.Repo != repo {
			continue
		}
		if !since.IsZero() {
			tStr := rec.ClosedAt
			if tStr == "" {
				tStr = rec.OpenedAt
			}
			if tStr != "" {
				if t, err := parseTime(tStr); err == nil && t.Before(since) {
					continue
				}
			}
		}

		names := make(map[string]bool)
		switch by {
		case "skill":
			for _, tRef := range rec.Tasks {
				if tRef.Instructions != nil {
					for _, sk := range tRef.Instructions.Skills {
						if sk.Name != "" {
							names[sk.Name] = true
						}
					}
				} else {
					if tr, ok := s.tasks[tRef.Sandbox+"/"+tRef.Task]; ok && tr.Instructions != nil {
						for _, sk := range tr.Instructions.Skills {
							if sk.Name != "" {
								names[sk.Name] = true
							}
						}
					}
				}
			}
			for _, tr := range s.tasks {
				if tr.PR == rec.PR && (repo == "" || tr.Repo == rec.Repo) && tr.Instructions != nil {
					for _, sk := range tr.Instructions.Skills {
						if sk.Name != "" {
							names[sk.Name] = true
						}
					}
				}
			}
		case "workflow":
			for _, tRef := range rec.Tasks {
				if tRef.Instructions != nil && tRef.Instructions.Workflow != nil && tRef.Instructions.Workflow.Name != "" {
					names[tRef.Instructions.Workflow.Name] = true
				} else {
					if tr, ok := s.tasks[tRef.Sandbox+"/"+tRef.Task]; ok && tr.Instructions != nil && tr.Instructions.Workflow != nil && tr.Instructions.Workflow.Name != "" {
						names[tr.Instructions.Workflow.Name] = true
					}
				}
			}
			for _, tr := range s.tasks {
				if tr.PR == rec.PR && (repo == "" || tr.Repo == rec.Repo) && tr.Instructions != nil && tr.Instructions.Workflow != nil && tr.Instructions.Workflow.Name != "" {
					names[tr.Instructions.Workflow.Name] = true
				}
			}
			for _, ur := range s.records {
				if ur.PR == rec.PR && (repo == "" || ur.Repo == rec.Repo) {
					if ur.WorkflowName != "" {
						names[ur.WorkflowName] = true
					} else if ur.Workflow != "" {
						names[ur.Workflow] = true
					}
				}
			}
		}

		prTokens := s.tokensForPR(rec.Repo, rec.PR)
		isMerged := rec.State == "merged"
		isBotPR := rec.OpenedByBot || rec.HasSessionLink
		hasHumanEdit := rec.HumanEdit != nil && (rec.HumanEdit.Files > 0 || rec.HumanEdit.Added > 0 || rec.HumanEdit.Deleted > 0 || len(rec.HumanEdit.Paths) > 0)

		for name := range names {
			agg, ok := aggs[name]
			if !ok {
				agg = &itemAgg{}
				aggs[name] = agg
			}
			agg.totalPRs++
			if isMerged {
				agg.mergedPRs++
			}
			if isBotPR {
				agg.botTotalPRs++
				if isMerged {
					agg.botMergedPRs++
				}
			}
			agg.carePasses += rec.CarePasses
			if hasHumanEdit {
				agg.humanEdits++
			}
			agg.totalTokens += prTokens
		}
	}

	var out []OutcomeRollup
	for name, agg := range aggs {
		rate := 0.0
		if agg.botTotalPRs > 0 {
			rate = float64(agg.botMergedPRs) / float64(agg.botTotalPRs)
		}
		costPerMerged := 0.0
		if agg.mergedPRs > 0 {
			costPerMerged = float64(agg.totalTokens) / float64(agg.mergedPRs)
		}
		out = append(out, OutcomeRollup{
			Name:            name,
			TotalPRs:        agg.totalPRs,
			MergedPRs:       agg.mergedPRs,
			AcceptanceRate:  rate,
			CarePasses:      agg.carePasses,
			HumanEdits:      agg.humanEdits,
			CostPerMergedPR: costPerMerged,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out, nil
}
