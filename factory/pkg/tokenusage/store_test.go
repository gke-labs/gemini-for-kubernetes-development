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
	"testing"
)

func statsFor(model string, input, output, total int64) Stats {
	return Stats{Models: map[string]ModelUsage{
		model: {
			API:    APIUsage{TotalRequests: 1},
			Tokens: TokenUsage{Input: input, Output: output, Total: total},
		},
	}}
}

func newTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, dir
}

func TestPutReplayAndUpsert(t *testing.T) {
	s, dir := newTestStore(t)

	rec := UsageRecord{
		Key: "sb1:/workspaces/tasks/fix-1", Repo: "org/repo", TaskType: "fix",
		Sandbox: "sb1", Issue: 7, Stats: statsFor("gemini-2.5-pro", 100, 50, 150),
	}
	if err := s.Put(rec); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// Identical re-post is a no-op; updated stats upsert.
	if err := s.Put(rec); err != nil {
		t.Fatalf("Put identical: %v", err)
	}
	rec.Stats = statsFor("gemini-2.5-pro", 200, 80, 280)
	if err := s.Put(rec); err != nil {
		t.Fatalf("Put updated: %v", err)
	}
	if got := len(s.List(ListFilter{})); got != 1 {
		t.Fatalf("expected 1 record after upsert, got %d", got)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Replay: last line wins.
	s2, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore replay: %v", err)
	}
	defer func() { _ = s2.Close() }()
	recs := s2.List(ListFilter{})
	if len(recs) != 1 {
		t.Fatalf("expected 1 record after replay, got %d", len(recs))
	}
	if got := recs[0].Stats.Models["gemini-2.5-pro"].Tokens.Total; got != 280 {
		t.Errorf("expected replayed total 280, got %d", got)
	}
}

func TestListFilters(t *testing.T) {
	s, _ := newTestStore(t)
	mustPut(t, s, UsageRecord{Key: "a:1", Repo: "org/repo", Issue: 1, Stats: statsFor("m", 1, 1, 2)})
	mustPut(t, s, UsageRecord{Key: "b:1", Repo: "org/repo", PR: 5, Issues: []int{1}, Stats: statsFor("m", 1, 1, 2)})
	mustPut(t, s, UsageRecord{Key: "c:1", Repo: "org/other", PR: 9, Stats: statsFor("m", 1, 1, 2)})

	if got := len(s.List(ListFilter{Repo: "org/repo"})); got != 2 {
		t.Errorf("repo filter: expected 2, got %d", got)
	}
	// Issue filter matches direct issue and PR-referenced issues.
	if got := len(s.List(ListFilter{Issue: 1})); got != 2 {
		t.Errorf("issue filter: expected 2, got %d", got)
	}
	if got := len(s.List(ListFilter{PR: 9})); got != 1 {
		t.Errorf("pr filter: expected 1, got %d", got)
	}
	if got := len(s.List(ListFilter{Limit: 1})); got != 1 {
		t.Errorf("limit: expected 1, got %d", got)
	}
}

func TestRollups(t *testing.T) {
	s, _ := newTestStore(t)
	// Workflow task on issue 42.
	mustPut(t, s, UsageRecord{
		Key: "wf-issue-42:t1", Repo: "org/repo", Issue: 42,
		Workflow: "issue-42", WorkflowName: "greenfield",
		Stats: statsFor("gemini-2.5-pro", 100, 50, 150),
	})
	// PR task referencing workflow issue 42 but without a workflow tag: must
	// be absorbed into workflow issue-42 (and appear nowhere else).
	mustPut(t, s, UsageRecord{
		Key: "fix-repo-42:t2", Repo: "org/repo", PR: 101, Issues: []int{42},
		Stats: statsFor("gemini-2.5-pro", 10, 5, 15),
	})
	// Standalone PR (no issue, no workflow).
	mustPut(t, s, UsageRecord{
		Key: "pr-7:t3", Repo: "org/repo", PR: 7,
		Stats: statsFor("gemini-2.5-flash", 1, 1, 2),
	})
	// Non-workflow issue sandbox that produced a PR ("issue/PR sandbox").
	mustPut(t, s, UsageRecord{
		Key: "fix-repo-55:t4", Repo: "org/repo", Issue: 55, PR: 200,
		Stats: statsFor("gemini-2.5-pro", 20, 10, 30),
	})
	// Review of that PR, linked back to issue 55: counts toward the issue.
	mustPut(t, s, UsageRecord{
		Key: "factory-pr-200:t5", Repo: "org/repo", PR: 200, Issues: []int{55},
		Stats: statsFor("gemini-2.5-pro", 5, 2, 7),
	})

	// The three rollups are mutually exclusive.
	issues := s.RollupByIssue("org/repo")
	if len(issues) != 1 || issues[0].Key != "55" {
		t.Fatalf("issue rollup: expected only non-workflow issue 55, got %+v", issues)
	}
	if issues[0].TaskCount != 2 || len(issues[0].Records) != 2 {
		t.Errorf("issue 55 rollup: expected 2 tasks with records, got %+v", issues[0])
	}
	if got := issues[0].Stats.Models["gemini-2.5-pro"].Tokens.Total; got != 37 {
		t.Errorf("issue 55 rollup: expected total 37, got %d", got)
	}
	if len(issues[0].PRs) != 1 || issues[0].PRs[0] != 200 {
		t.Errorf("issue 55 rollup PRs: expected [200], got %v", issues[0].PRs)
	}

	prs := s.RollupByPR("org/repo")
	if len(prs) != 1 || prs[0].Key != "7" {
		t.Fatalf("pr rollup: expected only standalone PR 7, got %+v", prs)
	}
	if len(prs[0].Records) != 1 {
		t.Errorf("pr 7 rollup: expected records included, got %+v", prs[0])
	}

	wfs := s.RollupByWorkflow("org/repo")
	if len(wfs) != 1 {
		t.Fatalf("workflow rollup: expected 1, got %+v", wfs)
	}
	wf := wfs[0]
	if wf.Key != "issue-42" || wf.WorkflowName != "greenfield" {
		t.Errorf("workflow rollup identity: got %+v", wf)
	}
	if wf.TaskCount != 2 || len(wf.Records) != 2 {
		t.Errorf("workflow absorption: expected 2 tasks with records, got taskCount=%d records=%d", wf.TaskCount, len(wf.Records))
	}
	if got := wf.Stats.Models["gemini-2.5-pro"].Tokens.Total; got != 165 {
		t.Errorf("workflow rollup: expected total 165, got %d", got)
	}
	if len(wf.PRs) != 1 || wf.PRs[0] != 101 {
		t.Errorf("workflow rollup PRs: expected [101], got %v", wf.PRs)
	}

	detail := s.WorkflowRollup("org/repo", "issue-42", true)
	if detail == nil || len(detail.Records) != 2 {
		t.Fatalf("workflow detail: expected 2 records, got %+v", detail)
	}
	if s.WorkflowRollup("org/repo", "issue-999", true) != nil {
		t.Error("expected nil rollup for unknown workflow")
	}
}

func TestPutPreservesFirstRecordedAt(t *testing.T) {
	s, _ := newTestStore(t)
	rec := UsageRecord{
		Key: "sb:/workspaces/tasks/fix-1", Repo: "org/repo", RecordedAt: "2026-07-13T10:00:00Z",
		Stats: statsFor("m", 1, 1, 2),
	}
	mustPut(t, s, rec)
	// Sweep re-post with a later timestamp but identical stats: must be a
	// no-op keeping the original day bucket.
	rec.RecordedAt = "2026-07-15T10:00:00Z"
	mustPut(t, s, rec)
	got := s.List(ListFilter{})[0]
	if got.RecordedAt != "2026-07-13T10:00:00Z" {
		t.Errorf("expected first RecordedAt preserved, got %s", got.RecordedAt)
	}
	// Updated stats still keep the original timestamp.
	rec.Stats = statsFor("m", 2, 2, 4)
	rec.RecordedAt = "2026-07-16T10:00:00Z"
	mustPut(t, s, rec)
	got = s.List(ListFilter{})[0]
	if got.RecordedAt != "2026-07-13T10:00:00Z" || got.Stats.Models["m"].Tokens.Total != 4 {
		t.Errorf("expected updated stats with original RecordedAt, got %+v", got)
	}
}

func TestRollupByDay(t *testing.T) {
	s, _ := newTestStore(t)
	mustPut(t, s, UsageRecord{Key: "a:1", Repo: "org/repo", RecordedAt: "2026-07-13T10:00:00Z", Stats: statsFor("m", 10, 5, 15)})
	mustPut(t, s, UsageRecord{Key: "b:1", Repo: "org/repo", RecordedAt: "2026-07-13T23:59:00Z", Stats: statsFor("m", 1, 1, 2)})
	mustPut(t, s, UsageRecord{Key: "c:1", Repo: "org/repo", RecordedAt: "2026-07-14T00:01:00Z", Stats: statsFor("m", 3, 3, 6)})

	days := s.RollupByDay("org/repo")
	if len(days) != 2 {
		t.Fatalf("expected 2 days, got %+v", days)
	}
	// Newest day first.
	if days[0].Key != "2026-07-14" || days[1].Key != "2026-07-13" {
		t.Errorf("expected newest-first days, got %s, %s", days[0].Key, days[1].Key)
	}
	if days[1].TaskCount != 2 || days[1].Stats.Models["m"].Tokens.Total != 17 {
		t.Errorf("2026-07-13 rollup: got %+v", days[1])
	}
	if len(days[0].Records) != 1 {
		t.Errorf("expected records in daily rollup, got %+v", days[0])
	}
}

func TestSubjects(t *testing.T) {
	s, dir := newTestStore(t)
	mustPut(t, s, UsageRecord{Key: "fix-55:t1", Repo: "org/repo", Issue: 55, PR: 200, Stats: statsFor("m", 1, 1, 2)})
	mustPut(t, s, UsageRecord{Key: "wf-42:t1", Repo: "org/repo", Issue: 42, Workflow: "issue-42", Stats: statsFor("m", 1, 1, 2)})

	if err := s.PutSubject(Subject{
		Key: "issue-55", Repo: "org/repo", Kind: "issue", Number: 55,
		State: "open", CreatedAt: "2026-07-10T00:00:00Z",
	}); err != nil {
		t.Fatalf("PutSubject: %v", err)
	}
	// Later "closed" update without createdAt must keep the creation time.
	if err := s.PutSubject(Subject{
		Key: "issue-55", State: "closed", ClosedAt: "2026-07-14T00:00:00Z",
	}); err != nil {
		t.Fatalf("PutSubject update: %v", err)
	}
	if err := s.PutSubject(Subject{
		Key: "issue-42", Kind: "issue", Number: 42, State: "open", CreatedAt: "2026-07-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("PutSubject wf: %v", err)
	}
	if err := s.PutSubject(Subject{Key: ""}); err == nil {
		t.Error("expected error for empty subject key")
	}

	issues := s.RollupByIssue("org/repo")
	if len(issues) != 1 || issues[0].Key != "55" {
		t.Fatalf("issue rollup: got %+v", issues)
	}
	if issues[0].State != "closed" || issues[0].CreatedAt != "2026-07-10T00:00:00Z" || issues[0].ClosedAt != "2026-07-14T00:00:00Z" {
		t.Errorf("issue rollup subject join: got %+v", issues[0])
	}

	wfs := s.RollupByWorkflow("org/repo")
	if len(wfs) != 1 || wfs[0].State != "open" || wfs[0].CreatedAt != "2026-07-01T00:00:00Z" {
		t.Errorf("workflow rollup subject join: got %+v", wfs)
	}

	// Subjects persist across restart.
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	s2, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = s2.Close() }()
	issues = s2.RollupByIssue("org/repo")
	if len(issues) != 1 || issues[0].State != "closed" || issues[0].CreatedAt != "2026-07-10T00:00:00Z" {
		t.Errorf("subject replay: got %+v", issues)
	}
}

func TestMarkSummarized(t *testing.T) {
	s, dir := newTestStore(t)
	already, err := s.MarkSummarized("issue-42")
	if err != nil || already {
		t.Fatalf("first mark: expected (false, nil), got (%v, %v)", already, err)
	}
	already, err = s.MarkSummarized("issue-42")
	if err != nil || !already {
		t.Fatalf("second mark: expected (true, nil), got (%v, %v)", already, err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Persists across restart.
	s2, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = s2.Close() }()
	already, err = s2.MarkSummarized("issue-42")
	if err != nil || !already {
		t.Fatalf("mark after restart: expected (true, nil), got (%v, %v)", already, err)
	}
}

func mustPut(t *testing.T, s *Store, rec UsageRecord) {
	t.Helper()
	if err := s.Put(rec); err != nil {
		t.Fatalf("Put %s: %v", rec.Key, err)
	}
}
