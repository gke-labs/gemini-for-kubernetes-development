package commands

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strings"

	githubv39 "github.com/google/go-github/v39/github"
	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/recipe"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/watch/rules"
)

// repoWatchDir is where a repository keeps its watch rules, one file per
// rule (design/watch-rules.md).
const repoWatchDir = ".agents/watch"

// repoDirFiles reads the files of dir in owner/repo, by file name, all at
// one commit, and says which: EnvRepoRecipeCommit's (latest is the default
// branch's head), from the clone in EnvRepoRecipeDir when it has it, else
// through GitHub's API. A repository without dir has no files.
func repoDirFiles(ctx context.Context, gh *githubv39.Client, owner, repo, dir string) (map[string][]byte, string, error) {
	if clone := strings.TrimSpace(os.Getenv(EnvRepoRecipeDir)); clone != "" {
		if rev := localRev(ctx, clone, repoRecipeCommit()); rev != "" {
			files, err := localDirFiles(ctx, clone, rev, dir)
			if err == nil {
				return files, rev, nil
			}
			klog.Warningf("reading %s from the clone in %s at %s: %v; reading it from GitHub", dir, clone, rev, err)
		}
	}
	return apiDirFiles(ctx, gh, owner, repo, dir)
}

// localDirFiles is dir's files at rev in the clone in clone's git objects.
func localDirFiles(ctx context.Context, clone, rev, dir string) (map[string][]byte, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", clone, "ls-tree", "--name-only", rev, "--", dir+"/").Output()
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	for _, path := range strings.Fields(string(out)) {
		data, err := exec.CommandContext(ctx, "git", "-C", clone, "show", rev+":"+path).Output()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		files[strings.TrimPrefix(path, dir+"/")] = data
	}
	return files, nil
}

// apiDirFiles is dir's files through GitHub's API, at EnvRepoRecipeCommit's
// commit, or the default branch's head resolved to its SHA.
func apiDirFiles(ctx context.Context, gh *githubv39.Client, owner, repo, dir string) (map[string][]byte, string, error) {
	rev := repoRecipeCommit()
	if rev == "latest" {
		sha, _, err := gh.Repositories.GetCommitSHA1(ctx, owner, repo, "HEAD", "")
		if err != nil {
			return nil, "", fmt.Errorf("resolving %s/%s's default branch head: %w", owner, repo, err)
		}
		rev = sha
	}
	opts := &githubv39.RepositoryContentGetOptions{Ref: rev}
	_, entries, resp, err := gh.Repositories.GetContents(ctx, owner, repo, dir, opts)
	if resp != nil && resp.StatusCode == http.StatusNotFound {
		return map[string][]byte{}, rev, nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("listing %s/%s %s: %w", owner, repo, dir, err)
	}
	files := map[string][]byte{}
	for _, e := range entries {
		if e.GetType() != "file" {
			continue
		}
		fc, _, _, err := gh.Repositories.GetContents(ctx, owner, repo, e.GetPath(), opts)
		if err != nil {
			return nil, "", fmt.Errorf("reading %s/%s %s: %w", owner, repo, e.GetPath(), err)
		}
		content, err := fc.GetContent()
		if err != nil {
			return nil, "", err
		}
		files[e.GetName()] = []byte(content)
	}
	return files, rev, nil
}

// repoWatchRules is the watch's rules for owner/repo: factory's (base) with
// the repository's .agents/watch laid over them as the repository recipe
// mode says (rules.Compose). Unset is off here, unlike for recipes: an
// overseer that has not opted in (spec.repoRecipe) runs factory's rules.
// from says where the repository's rules came from, "" when none were read.
func repoWatchRules(owner, repo string) func(ctx context.Context, base rules.Set) (rules.Set, string, error) {
	return func(ctx context.Context, base rules.Set) (rules.Set, string, error) {
		mode := repoRecipeMode()
		if mode == "" || mode == "off" {
			return base, "", nil
		}
		gh, err := github.NewClient(ctx)
		if err != nil {
			return nil, "", fmt.Errorf("creating github client: %w", err)
		}
		files, rev, err := repoDirFiles(ctx, gh, owner, repo, repoWatchDir)
		if err != nil {
			return nil, "", err
		}
		recipeFiles, _, err := repoDirFiles(ctx, gh, owner, repo, repoRecipesDir)
		if err != nil {
			return nil, "", err
		}
		var repoRecipes []string
		for file := range recipeFiles {
			if name, ok := strings.CutSuffix(file, ".yaml"); ok {
				repoRecipes = append(repoRecipes, name)
			}
		}
		set, skipped, n, err := composeRepoWatchRules(base, files, repoRecipes, mode)
		if err != nil {
			return nil, "", fmt.Errorf("%s at %s: %w", repoWatchDir, rev, err)
		}
		for _, s := range skipped {
			klog.Warningf("The repository's watch rule %s (%s at %s): left out", s, repoWatchDir, rev)
		}
		return set, fmt.Sprintf("%s@%s (%s, %d rules)", repoWatchDir, rev, mode, n), nil
	}
}

// composeRepoWatchRules lays a repository's rule files over base as mode
// says (rules.Compose), and says which it left out and why, and how many
// files were rules. A rule whose recipe is neither factory's nor one of
// repoRecipes (the repository's .agents/recipes, by name) is left out: its
// tasks would only fail.
func composeRepoWatchRules(base rules.Set, files map[string][]byte, repoRecipes []string, mode string) (rules.Set, []string, int, error) {
	repoRules, err := rules.LoadFiles(files)
	if err != nil {
		return nil, nil, 0, err
	}
	var known []*rules.Rule
	var skipped []string
	for _, r := range repoRules {
		if name := r.Run.Recipe; name != "" && !slices.Contains(recipe.BuiltinNames(), name) && !slices.Contains(repoRecipes, name) {
			skipped = append(skipped, fmt.Sprintf("%s: runs recipe %s, which is neither factory's nor in the repository's %s", r.Name, name, repoRecipesDir))
			continue
		}
		known = append(known, r)
	}
	set, more, err := rules.Compose(base, known, mode)
	if err != nil {
		return nil, nil, 0, err
	}
	return set, append(skipped, more...), len(repoRules), nil
}
