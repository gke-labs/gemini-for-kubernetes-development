package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/retro"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/spool"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/taskapi"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/taskoutput"
	githubv39 "github.com/google/go-github/v39/github"
)

// retroPRRecipe is the per-PR retro's recipe (recipe/recipes/retro-pr.yaml).
const retroPRRecipe = "retro-pr"

const deleteSandboxTimeout = 30 * time.Second

var (
	errPermanentRetroOutput = errors.New("retro task output is unusable")
	retroPRListBackoff      = []time.Duration{time.Second, 2 * time.Second}
	retroPRRetryWait        = sleepContext
)

// watchPRRetro is the watch's per-PR retro (watch.Watcher.RunPRRetro,
// design/retro-per-pr.md): it stages the PR here, in the caller - the
// sandbox never reaches the collector - and, unless there is nothing to
// retro on, runs the retro-pr recipe on it as user, in a sandbox of the
// retro's own, and opens its Change as a ready PR with user's token. What
// came of it - the PR it opened, or why it changed nothing - is read back
// from the task's Change; the watch says it on the PR. The sandbox is
// deleted once the retro has ended. What a later try can mend - a
// failure before the task started, or in applying or reading back the
// result of one that ended well - is retro.TryAgain, and leaves a started
// run's sandbox for the next try to pick the run up in; a watch stopping
// under it leaves it too.
func watchPRRetro(ctx context.Context, repoURL, user string, run retro.PRRun) (retro.PRResult, error) {
	owner, repo, err := parseGitHubRepoURL(repoURL)
	if err != nil {
		return retro.PRResult{}, err
	}
	dir, err := os.MkdirTemp("", "retro-pr-")
	if err != nil {
		return retro.PRResult{}, err
	}
	defer os.RemoveAll(dir)
	st, err := retro.StagePR(ctx, retro.PRConfig{
		Collector: os.Getenv("COLLECTOR_URL"),
		Owner:     owner, Repo: repo,
		PR: run.PR, Notes: run.Notes, Feedback: run.Feedback,
		Dir: dir,
	})
	if err != nil {
		return retro.PRResult{}, retro.TryAgain(fmt.Errorf("staging PR #%d: %w", run.PR.Number, err))
	}
	res := retro.PRResult{Staged: st}
	if st.Empty() {
		return res, nil
	}
	var ensured, sandboxName, taskID string
	opts := recipeRunOpts{
		files:        &taskapi.Files{Dir: dir, To: "staged"},
		sandboxReady: func(sb string) { ensured = sb },
		started: func(sb, task string) {
			sandboxName, taskID = sb, task
			res.Session = sessionLink(configuredSessionURL(), &taskoutput.Document{Source: taskoutput.Source{Sandbox: sb, Task: task}}, sb)
			if run.Started != nil {
				run.Started(st, res.Session, user)
			}
		},
	}
	// Not pr_url: that retargets a Change at the PR (fillChange), which a
	// retro's never is.
	inputs := map[string]string{
		"original_pr":     strconv.Itoa(run.PR.Number),
		"original_pr_url": run.PR.URL,
		"draft":           "false",
		"labels":          strings.Join(run.Labels, ","),
	}
	runErr := runRecipeIn(ctx, opts, retroPRRecipe, repoURL, "retro-pr-"+run.Key, "pr-"+run.Key, user, false,
		retroApplyMode(true, false, user), inputs, nil)
	if ctx.Err() != nil {
		if sandboxName == "" && ensured != "" && !run.Resume {
			deleteRetroSandbox(ctx, ensured)
		}
		return res, ctx.Err()
	}
	if sandboxName == "" {
		// No task started or was picked up: a sandbox made for this try
		// alone goes, and the retro is tried again. One a retro may have
		// started in before is left for the next try to find its run in.
		if ensured != "" && !run.Resume {
			deleteRetroSandbox(ctx, ensured)
		}
		if runErr == nil {
			runErr = errors.New("its task never started")
		}
		return res, retro.TryAgain(runErr)
	}
	if runErr != nil {
		if retroTaskFailed(ctx, sandboxName, taskID) {
			// The retro itself failed (or exited 0 without usable
			// task output): that is its end.
			deleteRetroSandbox(ctx, sandboxName)
			return res, runErr
		}
		// It ended well, or runs on, and applying or following it failed:
		// the next try picks the run up and applies it.
		return res, retro.TryAgain(runErr)
	}
	c, err := retroPRChange(ctx, sandboxName, taskID)
	if err != nil {
		if errors.Is(err, errPermanentRetroOutput) {
			deleteRetroSandbox(ctx, sandboxName)
			return res, err
		}
		return res, retro.TryAgain(err)
	}
	switch {
	case c.Fork != "" && c.Branch != "":
		if res.PRURL, err = retroPROpened(ctx, owner, repo, c); err != nil {
			return res, retro.TryAgain(err)
		}
	case c.Comment != "":
		res.Comment = c.Comment
	default:
		deleteRetroSandbox(ctx, sandboxName)
		return res, fmt.Errorf("the retro's Change opens no PR and says nothing")
	}
	deleteRetroSandbox(ctx, sandboxName)
	return res, nil
}

// retroTaskFailed reports whether task taskID of sandboxName ended in a
// failure that another try cannot mend: a non-zero exit, or a zero exit
// that left missing or unusable Change output. False when it runs on,
// cannot be read, or produced a valid Change whose apply may have failed
// transiently.
func retroTaskFailed(ctx context.Context, sandboxName, taskID string) bool {
	sb, err := taskapi.Connect(ctx, rootFlags.Namespace, sandboxName)
	if err != nil {
		return false
	}
	defer sb.Close()
	sel := taskSelectFlags{id: taskID}
	e, err := sel.find(ctx, sb)
	if err != nil {
		return false
	}
	return retroEntryFailed(taskID, e, func() ([]byte, error) {
		return readTaskOutput(ctx, sb, e)
	})
}

func retroEntryFailed(taskID string, e spool.Entry, read func() ([]byte, error)) bool {
	if e.State != spool.Exited {
		return false
	}
	if e.ExitCode != "0" {
		return true
	}
	data, readErr := read()
	if readErr != nil {
		return false
	}
	_, changeErr := parseRetroChange(taskID, data)
	return errors.Is(changeErr, errPermanentRetroOutput)
}

func parseRetroChange(taskID string, data []byte) (*taskoutput.Change, error) {
	if data == nil {
		return nil, fmt.Errorf("%w: task %s left no result", errPermanentRetroOutput, taskID)
	}
	docs, err := taskoutput.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%w: task %s: %v", errPermanentRetroOutput, taskID, err)
	}
	for _, d := range docs {
		if d.Kind == "Change" {
			c, err := d.ChangeSpec()
			if err != nil {
				return nil, fmt.Errorf("%w: task %s: %v", errPermanentRetroOutput, taskID, err)
			}
			return c, nil
		}
	}
	return nil, fmt.Errorf("%w: task %s left no Change", errPermanentRetroOutput, taskID)
}

// retroPRChange is the Change task taskID of sandboxName left.
func retroPRChange(ctx context.Context, sandboxName, taskID string) (*taskoutput.Change, error) {
	if sandboxName == "" || taskID == "" {
		return nil, fmt.Errorf("the retro's task is not known")
	}
	sb, err := taskapi.Connect(ctx, rootFlags.Namespace, sandboxName)
	if err != nil {
		return nil, fmt.Errorf("connecting to sandbox %s: %w", sandboxName, err)
	}
	defer sb.Close()
	sel := taskSelectFlags{id: taskID}
	e, err := sel.find(ctx, sb)
	if err != nil {
		return nil, err
	}
	data, err := readTaskOutput(ctx, sb, e)
	if err != nil {
		return nil, err
	}
	return parseRetroChange(taskID, data)
}

// retroPROpened is the PR open-pr opened from c's branch.
func retroPROpened(ctx context.Context, owner, repo string, c *taskoutput.Change) (string, error) {
	gh, err := github.NewClient(ctx)
	if err != nil {
		return "", fmt.Errorf("creating github client: %w", err)
	}
	return retroPROpenedWith(ctx, gh, owner, repo, c)
}

func retroPROpenedWith(ctx context.Context, gh *githubv39.Client, owner, repo string, c *taskoutput.Change) (string, error) {
	forkOwner, _, _ := strings.Cut(c.Fork, "/")
	head := forkOwner + ":" + c.Branch
	var lastErr error
	for attempt := 0; attempt <= len(retroPRListBackoff); attempt++ {
		if attempt > 0 {
			if err := retroPRRetryWait(ctx, retroPRListBackoff[attempt-1]); err != nil {
				return "", err
			}
		}
		prs, _, err := gh.PullRequests.List(ctx, owner, repo, &githubv39.PullRequestListOptions{State: "all", Head: head})
		if err != nil {
			lastErr = fmt.Errorf("looking for the retro's PR from %s: %w", head, err)
			continue
		}
		if len(prs) == 0 {
			lastErr = fmt.Errorf("no PR was opened from %s", head)
			continue
		}
		return prs[0].GetHTMLURL(), nil
	}
	return "", lastErr
}

func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// deleteRetroSandbox deletes a per-PR retro's sandbox: each retro has one
// of its own, which nothing uses after it. Its task was archived when it
// ended. A failure is logged, and the sweeps collect it later.
func deleteRetroSandbox(ctx context.Context, name string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), deleteSandboxTimeout)
	defer cancel()
	kubeClient, err := clients.NewKubernetesClient()
	if err == nil {
		err = k8s.NewManager(kubeClient).DeleteSandbox(ctx, rootFlags.Namespace, name)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Could not delete the retro's sandbox %s: %v\n", name, err)
		return
	}
	fmt.Printf("Deleted the retro's sandbox %s\n", name)
}
