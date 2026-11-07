package prs

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	githubv39 "github.com/google/go-github/v39/github"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/taskoutput"
)

type fakeDeferred struct {
	runs   map[int64]*githubv39.WorkflowRun
	reran  []int64
	edits  map[int64]string
	rerErr error
}

func (f *fakeDeferred) GetWorkflowRun(_ context.Context, id int64) (*githubv39.WorkflowRun, error) {
	if r, ok := f.runs[id]; ok {
		return r, nil
	}
	return nil, errors.New("404")
}
func (f *fakeDeferred) RerunFailedJobs(_ context.Context, id int64) error {
	if f.rerErr != nil {
		return f.rerErr
	}
	f.reran = append(f.reran, id)
	return nil
}
func (f *fakeDeferred) EditComment(_ context.Context, id int64, body string) error {
	f.edits[id] = body
	return nil
}

func run(id int64, status, conclusion string) *githubv39.WorkflowRun {
	return &githubv39.WorkflowRun{ID: githubv39.Int64(id), Status: githubv39.String(status), Conclusion: githubv39.String(conclusion)}
}

// A rerun a care-ci apply deferred is made once its run has completed and
// failed at the head it was judged at, once; a run that passed, a head that
// moved on, or a run past its time settles it unrerun; one still running
// waits. Only the watcher's and the pool's markers count.
func TestCarryOutDeferredReruns(t *testing.T) {
	now := time.Date(2026, 11, 4, 20, 20, 0, 0, time.UTC)
	pending := func(id int64, head string, until time.Time) string {
		return taskoutput.DeferredRerunComment(taskoutput.DeferredRerun{Run: id, Head: head, Until: until, State: taskoutput.DeferredPending}, "ci")
	}
	later := now.Add(time.Hour)
	comment := func(id int64, author, body string) *githubv39.IssueComment {
		return &githubv39.IssueComment{ID: githubv39.Int64(id), Body: githubv39.String(body), User: &githubv39.User{Login: githubv39.String(author)}}
	}
	comments := []*githubv39.IssueComment{
		comment(1, "neumann-coder-bot", pending(10, "b67a6e47", later)),                                                    // failed: rerun
		comment(2, "neumann-coder-bot", pending(11, "b67a6e47", later)),                                                    // passed: closed
		comment(3, "argus-watcher-bot", pending(12, "b67a6e47", later)),                                                    // still running: waits
		comment(4, "neumann-coder-bot", pending(13, "0ldhead1", later)),                                                    // head moved: closed
		comment(5, "neumann-coder-bot", pending(14, "b67a6e47", now.Add(-time.Minute))),                                    // running past its time: closed
		comment(6, "mallory", pending(15, "b67a6e47", later)),                                                              // not trusted: ignored
		comment(7, "neumann-coder-bot", strings.Replace(pending(16, "b67a6e47", later), "state=pending", "state=done", 1)), // done: ignored
	}
	f := &fakeDeferred{edits: map[int64]string{}, runs: map[int64]*githubv39.WorkflowRun{
		10: run(10, "completed", "failure"), 11: run(11, "completed", "success"), 12: run(12, "in_progress", ""),
		13: run(13, "completed", "failure"), 14: run(14, "queued", ""), 15: run(15, "completed", "failure"), 16: run(16, "completed", "failure"),
	}}
	pr := &githubv39.PullRequest{Number: githubv39.Int(14849), Head: &githubv39.PullRequestBranch{SHA: githubv39.String("b67a6e47abcdef")}}
	carryOutDeferredReruns(context.Background(), f, nil, pr, comments, []string{"argus-watcher-bot", "neumann-coder-bot"}, now, false)

	if len(f.reran) != 1 || f.reran[0] != 10 {
		t.Errorf("reran %v, want [10]", f.reran)
	}
	for id, want := range map[int64]string{1: "Re-ran the failed jobs of run 10 at 20:20Z", 2: "Not re-run: run 11 passed", 4: "moved on to b67a6e4", 5: "was still queued at 20:20Z"} {
		got, ok := taskoutput.ParseDeferredRerun(f.edits[id])
		wantState := taskoutput.DeferredClosed
		if id == 1 {
			wantState = taskoutput.DeferredDone
		}
		if !strings.Contains(f.edits[id], want) || !ok || got.State != wantState {
			t.Errorf("comment %d: %q (state %q), want %q, %s", id, f.edits[id], got.State, want, wantState)
		}
	}
	for _, id := range []int64{3, 6, 7} {
		if _, edited := f.edits[id]; edited {
			t.Errorf("comment %d was settled: %q", id, f.edits[id])
		}
	}

	// A rerun GitHub refuses leaves the marker pending for the next scan.
	f2 := &fakeDeferred{edits: map[int64]string{}, runs: map[int64]*githubv39.WorkflowRun{10: run(10, "completed", "failure")}, rerErr: errors.New("500")}
	carryOutDeferredReruns(context.Background(), f2, nil, pr, comments[:1], []string{"neumann-coder-bot"}, now, false)
	if len(f2.edits) != 0 {
		t.Errorf("settled after a failed rerun: %v", f2.edits)
	}
	// Dry run: nothing rerun, nothing edited.
	f3 := &fakeDeferred{edits: map[int64]string{}, runs: map[int64]*githubv39.WorkflowRun{10: run(10, "completed", "failure")}}
	carryOutDeferredReruns(context.Background(), f3, nil, pr, comments[:1], []string{"neumann-coder-bot"}, now, true)
	if len(f3.reran)+len(f3.edits) != 0 {
		t.Errorf("dry run wrote: %v %v", f3.reran, f3.edits)
	}
}

// A deferred rerun is made as the marker's author, the pool bot that
// deferred it (KCC #15213: the watcher's token got 403 "Must have admin
// rights"); one that keeps failing is settled unrerun once its time is
// past, not retried every scan for ever.
func TestDeferredRerunAsItsAuthor(t *testing.T) {
	now := time.Date(2026, 11, 7, 16, 0, 0, 0, time.UTC)
	pr := &githubv39.PullRequest{Number: githubv39.Int(15213), Head: &githubv39.PullRequestBranch{SHA: githubv39.String("95975227abc")}}
	marker := func(until time.Time) []*githubv39.IssueComment {
		body := taskoutput.DeferredRerunComment(taskoutput.DeferredRerun{Run: 42, Head: "95975227", Until: until, State: taskoutput.DeferredPending}, "ci")
		return []*githubv39.IssueComment{{ID: githubv39.Int64(1), Body: githubv39.String(body), User: &githubv39.User{Login: githubv39.String("ada-coder-bot")}}}
	}
	trusted := []string{"argus-watcher-bot", "ada-coder-bot"}

	f := &fakeDeferred{edits: map[int64]string{}, runs: map[int64]*githubv39.WorkflowRun{42: run(42, "completed", "failure")}}
	var as []string
	rerunAs := func(_ context.Context, _ int64, login string) error { as = append(as, login); return nil }
	carryOutDeferredReruns(context.Background(), f, rerunAs, pr, marker(now.Add(time.Hour)), trusted, now, false)
	if strings.Join(as, ",") != "ada-coder-bot" || len(f.reran) != 0 {
		t.Errorf("reran as %v (and with the watcher's client %v), want as ada-coder-bot alone", as, f.reran)
	}
	if got, _ := taskoutput.ParseDeferredRerun(f.edits[1]); got.State != taskoutput.DeferredDone {
		t.Errorf("not settled done: %q", f.edits[1])
	}

	refused := func(context.Context, int64, string) error {
		return errors.New("403 Must have admin rights to Repository")
	}
	f = &fakeDeferred{edits: map[int64]string{}, runs: map[int64]*githubv39.WorkflowRun{42: run(42, "completed", "failure")}}
	carryOutDeferredReruns(context.Background(), f, refused, pr, marker(now.Add(time.Hour)), trusted, now, false)
	if len(f.edits) != 0 {
		t.Errorf("settled within its time after a refused rerun: %v", f.edits)
	}
	carryOutDeferredReruns(context.Background(), f, refused, pr, marker(now.Add(-time.Minute)), trusted, now, false)
	if got, _ := taskoutput.ParseDeferredRerun(f.edits[1]); got.State != taskoutput.DeferredClosed || !strings.Contains(f.edits[1], "admin rights") {
		t.Errorf("a refused rerun past its time: %q", f.edits[1])
	}
}
