package run

import (
	"fmt"
	"strings"

	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
)

// execContext is everything a recipe needs to start. It is deliberately
// small: a recipe's variation belongs in its definition, not in the
// plumbing that launches it.
type execContext struct {
	Factory factorycli.Launcher
	Key     string
	Run     *boardv1alpha1.Run
	Board   *boardv1alpha1.RepoBoard
	Member  string
	Token   string
}

// launched is where the work went: the sandbox running it, and the task
// prefix under /workspaces/tasks that the run writes its record to.
// Both are needed to observe the run after a controller restart, when
// the launcher's in-memory result no longer exists.
type launched struct {
	Sandbox    string
	TaskPrefix string
}

// executor launches one recipe and says where to watch for it.
//
// These bindings are the seam where v2 still leans on v1's launcher.
// They exist so phase 1 can prove the Run object end to end without
// also rewriting the executor; when factory grows `factory run
// <recipe>`, this map collapses into a single call and the per-recipe
// functions disappear.
type executor func(execContext) (launched, error)

var executors = map[string]executor{
	"understand": runUnderstand,
}

func runUnderstand(e execContext) (launched, error) {
	if e.Run.Spec.Target != "repo" {
		return launched{}, fmt.Errorf("understand targets the repo, got %q", e.Run.Spec.Target)
	}
	owner, repo, err := splitRepoURL(e.Board.Spec.RepoURL)
	if err != nil {
		return launched{}, err
	}
	sandbox := factorycli.ExploreSandboxName(repo)
	where := launched{Sandbox: sandbox, TaskPrefix: "explore"}
	started := e.Factory.StartExplore(e.Key, factorycli.ExploreOptions{
		Namespace:   e.Member,
		SandboxName: sandbox,
		Kind:        "onboard",
		RepoURL:     fmt.Sprintf("https://github.com/%s/%s", owner, repo),
		GithubToken: e.Token,
		Engine:      boardEngine(e.Board),
	})
	if !started {
		// The launcher declines when that sandbox is already busy. That
		// is a wait, not a failure — the reconciler will try again.
		return where, errBusy{sandbox: sandbox}
	}
	return where, nil
}

// errBusy is a wait, not a verdict. A busy sandbox means someone else's
// task holds the workspace; failing the Run for that would turn a queue
// into an error.
type errBusy struct{ sandbox string }

func (e errBusy) Error() string { return "sandbox " + e.sandbox + " is busy" }

func boardEngine(board *boardv1alpha1.RepoBoard) string {
	if board.Spec.Sandbox.Engine == "claude" {
		return "claude"
	}
	return "gemini"
}

func splitRepoURL(repoURL string) (string, string, error) {
	trimmed := strings.TrimSuffix(strings.TrimSuffix(repoURL, "/"), ".git")
	parts := strings.Split(trimmed, "/")
	if len(parts) < 2 {
		return "", "", fmt.Errorf("cannot parse repoURL %q", repoURL)
	}
	return parts[len(parts)-2], parts[len(parts)-1], nil
}
