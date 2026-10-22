package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/recipe"
	factorysandbox "github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/sandbox"
)

// --sandbox-key goes with a recipe on a repository whose sandbox is of
// scope key, and only with one; an agent's recipe runs through factory
// agent create.
func TestCheckSandboxKey(t *testing.T) {
	repo := githubItem{Owner: "o", Repo: "r"}
	issue := githubItem{Owner: "o", Repo: "r", Number: 7}
	with := func(d recipe.SandboxDecl) *recipe.Recipe {
		return &recipe.Recipe{Name: "x", On: []string{recipe.OnRepo}, Sandbox: &d}
	}
	keyed := with(recipe.SandboxDecl{Prefix: "rsch", Scope: recipe.ScopeKey})
	perRepo := with(recipe.SandboxDecl{Prefix: "tools", Scope: recipe.ScopeRepo})
	warm := with(recipe.SandboxDecl{Kind: recipe.SandboxWarm})
	agent := with(recipe.SandboxDecl{Kind: recipe.SandboxAgent})
	for name, tc := range map[string]struct {
		rec  *recipe.Recipe
		it   githubItem
		opts recipeRunOpts
		key  string
		want string
	}{
		"keyed with a key":      {keyed, repo, recipeRunOpts{}, "s1", ""},
		"keyed without":         {keyed, repo, recipeRunOpts{}, "", "give it one with --sandbox-key"},
		"repo scope with a key": {perRepo, repo, recipeRunOpts{}, "k", "drop --sandbox-key"},
		"repo scope without":    {perRepo, repo, recipeRunOpts{}, "", ""},
		"warm with a key":       {warm, repo, recipeRunOpts{}, "k", "drop --sandbox-key"},
		"warm without":          {warm, repo, recipeRunOpts{}, "", ""},
		"agent by recipe":       {agent, repo, recipeRunOpts{}, "", "factory agent create"},
		"agent by agent create": {agent, repo, recipeRunOpts{ensure: noEnsure}, "", ""},
		"issue with a key":      {&recipe.Recipe{Name: "fix", On: []string{recipe.OnIssue}}, issue, recipeRunOpts{}, "k", "for a recipe on a repository"},
		"issue without":         {&recipe.Recipe{Name: "fix", On: []string{recipe.OnIssue}}, issue, recipeRunOpts{}, "", ""},
	} {
		err := checkSandboxKey(tc.rec, tc.it, tc.opts, tc.key)
		if (tc.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tc.want)) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
}

// The built-ins' sandboxes are named by their prefixes: research's
// rsch-<repo>-<hash>, a run's run-<repo>-<hash>, the key never in the name.
func TestBuiltinSandboxNames(t *testing.T) {
	for name, want := range map[string]string{"research": "rsch-k8s-config-connector-", "run-deploy": "run-k8s-config-connector-"} {
		_, rec, err := recipe.Builtin(name)
		if err != nil {
			t.Fatal(err)
		}
		got := factorysandbox.ScopedSandboxName(rec.Sandbox.Prefix, "k8s-config-connector", "deploy-gke-k8s1")
		if !strings.HasPrefix(got, want) || strings.Contains(got, "deploy-gke") {
			t.Errorf("%s = %s, want %s<hash>", name, got, want)
		}
	}
}

// noEnsure stands for factory agent create's sandbox.
var noEnsure ensureSandbox = func(context.Context, *clients.KubernetesClient, string, string, string) (string, error) {
	return "agent-x", nil
}
