package commands

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/recipe"
	githubv39 "github.com/google/go-github/v39/github"
)

// repoRecipesDir is where a repository keeps recipes of its own
// (design/repo-recipes.md): .agents/recipes/<name>.yaml, read at its
// default branch.
const repoRecipesDir = ".agents/recipes"

// The operator's say over the target repository's own recipes (the
// Overseer's spec.repoRecipe), as environment the watch's commands
// inherit:
//
//   - EnvRepoRecipeMode: off (factory's recipes only), augment (the
//     repository's recipes too, under names of their own; a file named as a
//     built-in is ignored) or replace (the repository's recipe of a name
//     before factory's, a built-in's name included). Unset is today's CLI
//     and board behaviour: built-ins first, and the repository's recipes
//     under names of their own, read at the default branch.
//   - EnvRepoRecipeCommit: the commit .agents/recipes is read at, a SHA or
//     latest (the default branch's head, also when unset).
const (
	EnvRepoRecipeMode   = "FACTORY_REPO_RECIPE_MODE"
	EnvRepoRecipeCommit = "FACTORY_REPO_RECIPE_COMMIT"
)

func repoRecipeMode() string {
	return strings.TrimSpace(os.Getenv(EnvRepoRecipeMode))
}

// repoRecipeCommit is EnvRepoRecipeCommit's value: "latest" when unset.
func repoRecipeCommit() string {
	if c := strings.TrimSpace(os.Getenv(EnvRepoRecipeCommit)); c != "" {
		return c
	}
	return "latest"
}

// repoRecipeNameRE is a repository recipe's name, and its file's.
var repoRecipeNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// repoFile reads a file of a repository at its default branch;
// os.ErrNotExist when it has none of that path.
type repoFile func(ctx context.Context, path string) ([]byte, error)

// githubRepoFile reads owner/repo's files through gh, at the default
// branch: what has been reviewed and merged, never a pull request's head;
// at EnvRepoRecipeCommit's commit when that names one.
func githubRepoFile(gh *githubv39.Client, owner, repo string) repoFile {
	var opts *githubv39.RepositoryContentGetOptions
	if c := repoRecipeCommit(); c != "latest" {
		opts = &githubv39.RepositoryContentGetOptions{Ref: c}
	}
	return func(ctx context.Context, path string) ([]byte, error) {
		fc, _, resp, err := gh.Repositories.GetContents(ctx, owner, repo, path, opts)
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			return nil, os.ErrNotExist
		}
		if err != nil {
			return nil, fmt.Errorf("reading %s/%s %s: %w", owner, repo, path, err)
		}
		if fc == nil {
			return nil, fmt.Errorf("%s/%s %s is a directory", owner, repo, path)
		}
		content, err := fc.GetContent()
		return []byte(content), err
	}
}

// resolveRecipe loads the recipe arg names: a file when it looks like one
// (a path, or a name ending in .yaml), else the built-in of that name,
// else readRepo's .agents/recipes/<arg>.yaml — the target repository's
// own. With no readRepo, or EnvRepoRecipeMode off, a name is a built-in's.
// In replace mode the repository's recipe of that name comes first,
// whatever the name (overrideRecipe).
func resolveRecipe(ctx context.Context, arg string, readRepo repoFile) ([]byte, *recipe.Recipe, error) {
	if strings.ContainsRune(arg, '/') || strings.HasSuffix(arg, ".yaml") || strings.HasSuffix(arg, ".yml") {
		data, err := os.ReadFile(arg)
		if err != nil {
			return nil, nil, err
		}
		rec, err := recipe.Parse(data)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", arg, err)
		}
		return data, rec, nil
	}
	if repoRecipeMode() == "off" {
		readRepo = nil
	}
	if readRepo != nil && repoRecipeMode() == "replace" && repoRecipeNameRE.MatchString(arg) {
		data, rec, ok, err := overrideRecipe(ctx, arg, readRepo)
		if ok || err != nil {
			return data, rec, err
		}
	}
	if data, rec, err := recipe.Builtin(arg); err == nil || readRepo == nil {
		return data, rec, err
	}
	if !repoRecipeNameRE.MatchString(arg) {
		return nil, nil, fmt.Errorf("%q is not a recipe name (want %s)", arg, repoRecipeNameRE)
	}
	path := repoRecipesDir + "/" + arg + ".yaml"
	data, err := readRepo(ctx, path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, fmt.Errorf("no recipe %s: not a built-in one, and the repository has no %s", arg, path)
	}
	if err != nil {
		return nil, nil, err
	}
	rec, err := parseRepoRecipe(arg, data)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	return data, rec, nil
}

// overrideRecipe is the repository's .agents/recipes/<name>.yaml at
// EnvRepoRecipeCommit's commit, when it has one (ok): in replace mode the
// operator handed the repository its recipes, so it may take any name, a
// built-in's included, and is held only to being named as its file is.
func overrideRecipe(ctx context.Context, name string, readRepo repoFile) (data []byte, rec *recipe.Recipe, ok bool, err error) {
	path := repoRecipesDir + "/" + name + ".yaml"
	data, err = readRepo(ctx, path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, false, nil
	}
	if err != nil {
		return nil, nil, false, err
	}
	if rec, err = recipe.Parse(data); err != nil {
		return nil, nil, false, fmt.Errorf("%s at %s: %w", path, repoRecipeCommit(), err)
	}
	if rec.Name != name {
		return nil, nil, false, fmt.Errorf("%s at %s: recipe is named %s, not %s as its file is", path, repoRecipeCommit(), rec.Name, name)
	}
	fmt.Fprintf(os.Stderr, "Using the repository's recipe %s (%s at %s)\n", name, path, repoRecipeCommit())
	return data, rec, true, nil
}

// parseRepoRecipe parses a repository's recipe file name.yaml. It must
// be named as its file is, may not take a built-in's name — the overseer
// and every board rely on those meaning what they do — and may not
// declare yet a sandbox of its own: a run sandbox runs as the deployer
// service account, which reaches the member's GCP project, and a
// repository recipe gets that only once a board allows it.
func parseRepoRecipe(name string, data []byte) (*recipe.Recipe, error) {
	rec, err := recipe.Parse(data)
	if err != nil {
		return nil, err
	}
	switch {
	case rec.Name != name:
		return nil, fmt.Errorf("recipe is named %s, not %s as its file is", rec.Name, name)
	case isBuiltin(name):
		return nil, fmt.Errorf("%s is a built-in recipe's name; a repository's recipe cannot take it", name)
	case rec.Sandbox != nil:
		return nil, fmt.Errorf("a repository's recipe cannot declare a sandbox: of its own yet")
	case builtinSession(rec.Session):
		return nil, fmt.Errorf("session %s is a built-in group's; a repository's recipe cannot join its conversation", rec.Session)
	}
	return rec, nil
}

// builtinSession reports whether tag is a built-in recipe's session tag.
func builtinSession(tag string) bool {
	if tag == "" {
		return false
	}
	for _, name := range recipe.BuiltinNames() {
		if _, r, err := recipe.Builtin(name); err == nil && r.Session == tag {
			return true
		}
	}
	return false
}

func isBuiltin(name string) bool {
	_, _, err := recipe.Builtin(name)
	return err == nil
}

// repoRecipe is one file of a repository's .agents/recipes: its recipe,
// or why it is not one.
type repoRecipe struct {
	name, path string
	data       []byte
	rec        *recipe.Recipe
	err        error
}

// listRepoRecipes reads every recipe file in owner/repo's .agents/recipes
// at its default branch, sorted by name. A repository with none has none.
func listRepoRecipes(ctx context.Context, gh *githubv39.Client, owner, repo string) ([]repoRecipe, error) {
	_, dir, resp, err := gh.Repositories.GetContents(ctx, owner, repo, repoRecipesDir, nil)
	if resp != nil && resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("listing %s/%s %s: %w", owner, repo, repoRecipesDir, err)
	}
	read := githubRepoFile(gh, owner, repo)
	var out []repoRecipe
	for _, e := range dir {
		name, ok := strings.CutSuffix(e.GetName(), ".yaml")
		if e.GetType() != "file" || !ok {
			continue
		}
		r := repoRecipe{name: name, path: e.GetPath()}
		if r.data, r.err = read(ctx, r.path); r.err == nil {
			r.rec, r.err = parseRepoRecipe(name, r.data)
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, nil
}
