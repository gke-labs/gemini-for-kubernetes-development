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

// repoRecipeNameRE is a repository recipe's name, and its file's.
var repoRecipeNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// repoFile reads a file of a repository at its default branch;
// os.ErrNotExist when it has none of that path.
type repoFile func(ctx context.Context, path string) ([]byte, error)

// githubRepoFile reads owner/repo's files through gh, at the default
// branch: what has been reviewed and merged, never a pull request's head.
func githubRepoFile(gh *githubv39.Client, owner, repo string) repoFile {
	return func(ctx context.Context, path string) ([]byte, error) {
		fc, _, resp, err := gh.Repositories.GetContents(ctx, owner, repo, path, nil)
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
// own. With no readRepo, a name is a built-in's.
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
