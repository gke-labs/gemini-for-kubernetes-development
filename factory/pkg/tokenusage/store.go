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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Store persists usage records as a JSONL append log and keeps a full
// in-memory index, rebuilt by replaying the log at startup. On replay the
// last line for a key wins, so upserts are plain appends.
type Store struct {
	mu         sync.Mutex
	root       string
	records    map[string]UsageRecord
	summarized map[string]bool // workflow session -> summary comment posted
	log        *os.File
}

func NewStore(root string) (*Store, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("creating storage root: %w", err)
	}
	s := &Store{
		root:       root,
		records:    map[string]UsageRecord{},
		summarized: map[string]bool{},
	}
	if err := s.replay(); err != nil {
		return nil, err
	}
	if err := s.loadSummarized(); err != nil {
		return nil, err
	}
	log, err := os.OpenFile(s.recordsPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("opening records log: %w", err)
	}
	s.log = log
	return s, nil
}

func (s *Store) recordsPath() string    { return filepath.Join(s.root, "records.jsonl") }
func (s *Store) summarizedPath() string { return filepath.Join(s.root, "summarized.json") }

func (s *Store) replay() error {
	f, err := os.Open(s.recordsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var rec UsageRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			// Skip corrupt lines (e.g. torn write on crash) rather than
			// refusing to start.
			continue
		}
		if rec.Key != "" {
			s.records[rec.Key] = rec
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

// Put upserts a record by key. Identical re-posts are skipped without
// touching the log.
func (s *Store) Put(rec UsageRecord) error {
	if rec.Key == "" {
		return fmt.Errorf("record key is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.records[rec.Key]; ok && reflect.DeepEqual(existing, rec) {
		return nil
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
	return s.rollup(repo, func(rec UsageRecord) []string {
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
	return s.rollup(repo, func(rec UsageRecord) []string {
		if rec.PR == 0 || recordInWorkflow(rec, wfIssues) || recordTouchesAnyIssue(rec) {
			return nil
		}
		return []string{strconv.Itoa(rec.PR)}
	})
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
	sortRollup(&r)
	return &r
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

func (s *Store) rollup(repo string, keysFor func(UsageRecord) []string) []Rollup {
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

// Close closes the append log.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.log.Close()
}
