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

// executor launches one recipe and returns the sandbox it runs in.
//
// These bindings are the seam where v2 still leans on v1's launcher.
// They exist so phase 1 can prove the Run object end to end without
// also rewriting the executor; when factory grows `factory run
// <recipe>`, this map collapses into a single call and the per-recipe
// functions disappear.
type executor func(execContext) (string, error)

var executors = map[string]executor{
	"understand": runUnderstand,
}

func runUnderstand(e execContext) (string, error) {
	if e.Run.Spec.Target != "repo" {
		return "", fmt.Errorf("understand targets the repo, got %q", e.Run.Spec.Target)
	}
	owner, repo, err := splitRepoURL(e.Board.Spec.RepoURL)
	if err != nil {
		return "", err
	}
	sandbox := factorycli.ExploreSandboxName(repo)
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
		return sandbox, fmt.Errorf("sandbox %s is busy; will retry", sandbox)
	}
	return sandbox, nil
}

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
