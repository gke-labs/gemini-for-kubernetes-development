package prs

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/taskoutput"
)

// deferredGitHub is what carrying out deferred reruns needs of GitHub.
type deferredGitHub interface {
	GetWorkflowRun(ctx context.Context, id int64) (*githubv39.WorkflowRun, error)
	RerunFailedJobs(ctx context.Context, id int64) error
	EditComment(ctx context.Context, commentID int64, body string) error
}

// carryOutDeferredReruns makes the reruns a care-ci apply deferred on the
// PR because their run was still running (taskoutput.DeferredRerun): a
// pending marker in a comment by the watcher or a pool bot. Once its run has
// completed and failed, at the head it was judged at, its failed jobs are
// rerun, once, as the marker's author (rerunAs: the PR's author, who
// deferred it and whom GitHub lets rerun it, where the watcher may not);
// nil rerunAs, with gh. A run that passed, a head that moved on, or one
// still not done past its time settles it unrerun. Each is said in the
// comment. A failed rerun is logged, not the scan's: the next scan tries
// again, until the marker's time is past, when it is settled unrerun.
func carryOutDeferredReruns(ctx context.Context, gh deferredGitHub, rerunAs func(ctx context.Context, run int64, login string) error, pr *githubv39.PullRequest, comments []*githubv39.IssueComment, trusted []string, now time.Time, dryRun bool) {
	head := pr.GetHead().GetSHA()
	for _, c := range comments {
		d, ok := taskoutput.ParseDeferredRerun(c.GetBody())
		if !ok || d.State != taskoutput.DeferredPending || !slices.ContainsFunc(trusted, func(l string) bool { return strings.EqualFold(l, c.GetUser().GetLogin()) }) {
			continue
		}
		state, note := "", ""
		switch run, err := gh.GetWorkflowRun(ctx, d.Run); {
		case err != nil:
			klog.Warningf("PR #%d: reading run %d of its deferred rerun: %v", pr.GetNumber(), d.Run, err)
			continue
		case !strings.HasPrefix(head, d.Head):
			state, note = taskoutput.DeferredClosed, fmt.Sprintf("Not re-run: the pull request moved on to %.7s since.", head)
		case run.GetStatus() != "completed" && now.After(d.Until):
			state, note = taskoutput.DeferredClosed, fmt.Sprintf("Not re-run: run %d was still %s at %s.", d.Run, run.GetStatus(), now.UTC().Format("15:04Z"))
		case run.GetStatus() != "completed":
			continue // not yet
		case run.GetConclusion() == "success":
			state, note = taskoutput.DeferredClosed, fmt.Sprintf("Not re-run: run %d passed.", d.Run)
		default:
			if dryRun {
				klog.Infof("[dry-run] PR #%d: would rerun the failed jobs of run %d", pr.GetNumber(), d.Run)
				continue
			}
			author := c.GetUser().GetLogin()
			var err error
			if rerunAs != nil {
				err = rerunAs(ctx, d.Run, author)
			} else {
				err = gh.RerunFailedJobs(ctx, d.Run)
			}
			if err != nil {
				klog.Warningf("PR #%d: rerunning the failed jobs of run %d as %s: %v", pr.GetNumber(), d.Run, author, err)
				if !now.After(d.Until) {
					continue
				}
				state, note = taskoutput.DeferredClosed, fmt.Sprintf("Not re-run: rerunning run %d as %s failed: %v", d.Run, author, err)
				break
			}
			klog.Infof("PR #%d: reran the failed jobs of run %d, as deferred", pr.GetNumber(), d.Run)
			state, note = taskoutput.DeferredDone, fmt.Sprintf("Re-ran the failed jobs of run %d at %s.", d.Run, now.UTC().Format("15:04Z"))
		}
		if dryRun {
			klog.Infof("[dry-run] PR #%d: would settle the deferred rerun of run %d: %s", pr.GetNumber(), d.Run, note)
			continue
		}
		if err := gh.EditComment(ctx, c.GetID(), taskoutput.SettleDeferredRerun(c.GetBody(), d, state, note)); err != nil {
			klog.Warningf("PR #%d: settling the deferred rerun of run %d: %v", pr.GetNumber(), d.Run, err)
		}
	}
}
