package commands

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
	corev1 "k8s.io/api/core/v1"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/constants"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/recipe"
	factorysandbox "github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/sandbox"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/spool"
)

func TestParseGitHubItemURL(t *testing.T) {
	for raw, want := range map[string]githubItem{
		"https://github.com/o/r/issues/12":         {Owner: "o", Repo: "r", Number: 12},
		"https://github.com/o/r/pull/34":           {Owner: "o", Repo: "r", Number: 34, IsPR: true},
		"https://github.com/o/r/pull/34/files":     {Owner: "o", Repo: "r", Number: 34, IsPR: true},
		"https://github.com/o/r/issues/12?foo=1#x": {Owner: "o", Repo: "r", Number: 12},
	} {
		got, err := parseGitHubItemURL(raw)
		if err != nil || got != want {
			t.Errorf("%s = %+v, %v; want %+v", raw, got, err, want)
		}
	}
	for _, raw := range []string{
		"https://github.com/o/r",
		"https://github.com/o/r/discussions/1",
		"https://github.com/o/r/issues/x",
		"https://example.com/o/r/issues/1",
	} {
		if _, err := parseGitHubItemURL(raw); err == nil {
			t.Errorf("%s parsed, want an error", raw)
		}
	}
}

func TestParseInputArgs(t *testing.T) {
	got, err := parseInputArgs([]string{"a=1", "b=x=y", "c="})
	if err != nil {
		t.Fatal(err)
	}
	if got["a"] != "1" || got["b"] != "x=y" || got["c"] != "" || len(got) != 3 {
		t.Errorf("inputs = %v", got)
	}
	if _, err := parseInputArgs([]string{"novalue"}); err == nil {
		t.Error("an input without = parsed")
	}
}

func TestLoadRecipe(t *testing.T) {
	if _, r, err := loadRecipe("triage"); err != nil || r.Name != "triage" {
		t.Errorf("built-in: %v, %v", r, err)
	}
	if _, _, err := loadRecipe("no-such-recipe"); err == nil {
		t.Error("unknown built-in loaded")
	}
	path := filepath.Join(t.TempDir(), "mine.yaml")
	if err := os.WriteFile(path, []byte("name: mine\nstart: {steps: [{run: 'true'}]}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, r, err := loadRecipe(path); err != nil || r.Name != "mine" {
		t.Errorf("file: %v, %v", r, err)
	}
}

// Every built-in recipe is a command of its own, `factory recipe <name>`,
// with a flag per input that shadows no other flag, but for the inputs
// only revises read.
func TestBuiltinRecipeCommands(t *testing.T) {
	root := NewRootCommand(context.Background())
	recipeCmd, _, err := root.Find([]string{"recipe"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range recipe.BuiltinNames() {
		if name == "run" || name == "exec" || name == "revise" || name == "list" {
			t.Errorf("built-in recipe %q takes the name of a recipe command", name)
			continue
		}
		cmd, _, err := recipeCmd.Find([]string{name})
		if err != nil || cmd.Name() != name {
			t.Errorf("factory recipe %s: no such command (%v)", name, err)
			continue
		}
		_, rec, err := recipe.Builtin(name)
		if err != nil {
			t.Fatal(err)
		}
		for in, decl := range rec.Inputs {
			flag := inputFlagName(in, decl)
			if root.PersistentFlags().Lookup(flag) != nil || flag == "url" || flag == "input" || flag == "run-name" {
				t.Errorf("recipe %s: input %s's flag --%s is taken", name, in, flag)
			}
			if decl.Revise {
				// fix's instruction: --instruction is its instructions'.
				owned := false
				for other, d := range rec.Inputs {
					owned = owned || (other != in && !d.Revise && inputFlagName(other, d) == flag)
				}
				if cmd.Flags().Lookup(flag) != nil && !owned {
					t.Errorf("factory recipe %s has --%s, which only its revises read", name, flag)
				}
				continue
			}
			if cmd.Flags().Lookup(flag) == nil {
				t.Errorf("factory recipe %s has no --%s", name, flag)
			}
		}
	}
	if len(recipe.BuiltinNames()) == 0 {
		t.Error("no built-in recipes")
	}
}

// --apply picks up the newest run of the recipe on the issue, whatever
// ran after it on other issues or with other recipes.
func TestLastRun(t *testing.T) {
	entry := func(id, recipeName, url string) spool.Entry {
		return spool.Entry{Task: spool.Task{ID: id, Recipe: recipeName, URL: url}}
	}
	entries := []spool.Entry{ // newest first, as spool.List gives them
		entry("recipe-triage-3", "triage", "https://github.com/o/r/issues/8"),
		entry("recipe-explain-2", "explain", "https://github.com/o/r/issues/7"),
		entry("fix-2", "", ""),
		entry("recipe-triage-2", "triage", "https://github.com/O/r/issues/7/"),
		entry("recipe-triage-1", "triage", "https://github.com/o/r/issues/7"),
	}
	if e, ok := lastRun(entries, "triage", "https://github.com/o/r/issues/7"); !ok || e.ID != "recipe-triage-2" {
		t.Errorf("lastRun = %s, %v; want recipe-triage-2", e.ID, ok)
	}
	if e, ok := lastRun(entries, "triage", "https://github.com/o/r/issues/9"); ok {
		t.Errorf("lastRun on an issue never run = %s; want none", e.ID)
	}
}

func TestRunByName(t *testing.T) {
	entry := func(id, runName, recipeName, url string) spool.Entry {
		return spool.Entry{Task: spool.Task{ID: id, RunName: runName, Recipe: recipeName, URL: url}}
	}
	entries := []spool.Entry{ // newest first
		entry("recipe-triage-3", "auto/b/7/2", "triage", "https://github.com/o/r/issues/7"),
		entry("recipe-explain-2", "request/x", "explain", "https://github.com/o/r/issues/7"),
		entry("recipe-triage-1", "request/y", "triage", "https://github.com/O/r/issues/7/"),
	}
	if e, ok, err := runByName(entries, "request/y", "triage", "https://github.com/o/r/issues/7"); err != nil || !ok || e.ID != "recipe-triage-1" {
		t.Errorf("runByName(request/y) = %s, %v, %v; want recipe-triage-1", e.ID, ok, err)
	}
	if e, ok, err := runByName(entries, "request/z", "triage", "https://github.com/o/r/issues/7"); err != nil || ok {
		t.Errorf("runByName of an unused id = %s, %v, %v; want none", e.ID, ok, err)
	}
	if _, ok, err := runByName(entries, "request/x", "triage", "https://github.com/o/r/issues/7"); err == nil || ok {
		t.Errorf("runByName of another recipe's id = %v, %v; want an error", ok, err)
	}
	if _, ok, err := runByName(entries, "request/y", "triage", "https://github.com/o/r/issues/8"); err == nil || ok {
		t.Errorf("runByName of another issue's id = %v, %v; want an error", ok, err)
	}
}

// A revise revises the task named, or the newest that ran a recipe with
// that revise, a revise of it included.
func TestRevisedTask(t *testing.T) {
	entry := func(id, recipeName, session string) spool.Entry {
		return spool.Entry{Task: spool.Task{ID: id, Recipe: recipeName, Session: session}}
	}
	entries := []spool.Entry{ // newest first
		entry("recipe-triage-3", "triage", ""),
		entry("fix-2", "", ""),
		entry("recipe-plan-2", "plan", "recipe-plan-1"),
		entry("recipe-plan-1", "plan", ""),
	}
	if e, err := revisedTask(entries, "", "plan", nil); err != nil || e.ID != "recipe-plan-2" {
		t.Errorf("newest = %s, %v; want recipe-plan-2", e.ID, err)
	}
	if e, err := revisedTask(entries, "recipe-plan-1", "plan", nil); err != nil || e.ID != "recipe-plan-1" {
		t.Errorf("named = %s, %v", e.ID, err)
	}
	if _, err := revisedTask(entries, "fix-2", "plan", nil); err == nil {
		t.Error("revised a task that ran no recipe")
	}
	if _, err := revisedTask(entries, "", "nope", nil); err == nil {
		t.Error("found a task for a revise no recipe has")
	}
	_, mine, err := recipe.Builtin("plan")
	if err != nil {
		t.Fatal(err)
	}
	mine.Name = "triage"
	if e, err := revisedTask(entries, "", "plan", mine); err != nil || e.ID != "recipe-triage-3" {
		t.Errorf("with --recipe = %s, %v; want the newest task of that recipe", e.ID, err)
	}
}

func TestParseRecipeTarget(t *testing.T) {
	for raw, want := range map[string]githubItem{
		"https://github.com/o/r":           {Owner: "o", Repo: "r"},
		"https://github.com/o/r.git":       {Owner: "o", Repo: "r"},
		"https://github.com/o/r/":          {Owner: "o", Repo: "r"},
		"https://github.com/o/r/issues/12": {Owner: "o", Repo: "r", Number: 12},
		"https://github.com/o/r/pull/34":   {Owner: "o", Repo: "r", Number: 34, IsPR: true},
	} {
		got, err := parseRecipeTarget(raw)
		if err != nil || got != want {
			t.Errorf("%s = %+v, %v; want %+v", raw, got, err, want)
		}
	}
	for _, raw := range []string{
		"https://github.com/o",
		"https://github.com/o/r/tree/main",
		"https://github.com/o/r;x",
		"https://example.com/o/r",
	} {
		if _, err := parseRecipeTarget(raw); err == nil {
			t.Errorf("%s parsed, want an error", raw)
		}
	}
}

func TestRecipeEnvKeepsACloneTokenOutOfTheEnvironment(t *testing.T) {
	secret := &corev1.Secret{Data: map[string][]byte{
		constants.KeyGithubToken: []byte("tok"),
		constants.KeyGithubLogin: []byte("member"),
	}}
	repo := githubItem{Owner: "o", Repo: "r"}
	env, secrets, err := recipeEnv(secret, repo, recipe.CredentialsClone)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range recipe.GitHubTokenEnv {
		if env[k] != "" {
			t.Errorf("env has %s", k)
		}
	}
	if secrets["GITHUB_TOKEN"] != "tok" || env["ISSUE_NUMBER"] != "" || env["PR_NUMBER"] != "" || env["REPO_NAME"] != "r" {
		t.Errorf("env %v, secrets %v", env, secrets)
	}
	env, secrets, err = recipeEnv(secret, githubItem{Owner: "o", Repo: "r", Number: 3}, "")
	if err != nil || env["GITHUB_TOKEN"] != "tok" || env["ISSUE_NUMBER"] != "3" || secrets != nil {
		t.Errorf("full: env %v, secrets %v, %v", env, secrets, err)
	}
}

func TestTakeSecrets(t *testing.T) {
	dir := t.TempDir()
	if got, err := takeSecrets(dir); err != nil || got != nil {
		t.Fatalf("none: %v, %v", got, err)
	}
	path := filepath.Join(dir, spool.SecretsFile)
	if err := os.WriteFile(path, []byte(`{"GITHUB_TOKEN":"tok","A":"1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := takeSecrets(dir)
	if err != nil || len(got) != 2 || got[0] != "A=1" || got[1] != "GITHUB_TOKEN=tok" {
		t.Fatalf("takeSecrets = %v, %v", got, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("%s left on disk: %v", spool.SecretsFile, err)
	}
}

func TestRecipeExecRefusesACloneRecipeWithATokenInItsEnvironment(t *testing.T) {
	dir := t.TempDir()
	data, _, err := recipe.Builtin("research")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, spool.RecipeFile)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, spool.SecretsFile), []byte(`{"GITHUB_TOKEN":"tok"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_TOKEN", "leaked")
	err = runRecipeExec(context.Background(), path, "", dir)
	if err == nil || !strings.Contains(err.Error(), "GH_TOKEN") {
		t.Fatalf("runRecipeExec = %v, want refused for GH_TOKEN", err)
	}
	if _, err := os.Stat(filepath.Join(dir, spool.SecretsFile)); !os.IsNotExist(err) {
		t.Errorf("the refused task left its secrets on disk: %v", err)
	}
}

func TestRecordedRunCarriesRevises(t *testing.T) {
	_, rec, err := recipe.Builtin("research")
	if err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	run := recordedRun(spool.Task{ID: "t1", RunName: "r1"}, rec, started)
	if run.Task != "t1" || run.Name != "r1" || !run.StartedAt.Equal(started) ||
		run.Recipe != "research" || run.Kind != "Notes" || run.State != "Running" || run.EndedAt != nil {
		t.Errorf("run = %+v", run)
	}
	if len(run.Revises) != 1 || run.Revises[0].ID != "notes" || run.Revises[0].Label != "Save notes" || run.Revises[0].Inputs != nil {
		t.Errorf("revises = %+v", run.Revises)
	}

	// A revise's inputs are recorded with it: care's iterate asks for an
	// instruction.
	_, rec, err = recipe.Builtin("care")
	if err != nil {
		t.Fatal(err)
	}
	run = recordedRun(spool.Task{ID: "t2"}, rec, started)
	byID := map[string]factorysandbox.RecordedRevise{}
	for _, rv := range run.Revises {
		byID[rv.ID] = rv
	}
	if !slices.Equal(byID["iterate"].Inputs, []string{"instruction"}) || byID["rebase"].Label != "Rebase" || byID["rebase"].Inputs != nil || run.Kind != "Change" {
		t.Errorf("care run = %+v", run)
	}
}

// A recipe runs only on what its on: names; my-pr is a PR the caller
// authored from their fork.
func TestCheckOn(t *testing.T) {
	recipeOn := func(on ...string) *recipe.Recipe { return &recipe.Recipe{Name: "r", On: on} }
	issue := githubItem{Owner: "o", Repo: "r", Number: 1}
	prItem := githubItem{Owner: "o", Repo: "r", Number: 2, IsPR: true}
	repo := githubItem{Owner: "o", Repo: "r"}
	pr := func(author, headFullName, headOwner string, fork bool) *githubv39.PullRequest {
		return &githubv39.PullRequest{
			User: &githubv39.User{Login: githubv39.String(author)},
			Head: &githubv39.PullRequestBranch{Repo: &githubv39.Repository{FullName: githubv39.String(headFullName), Fork: githubv39.Bool(fork), Owner: &githubv39.User{Login: githubv39.String(headOwner)}}},
			Base: &githubv39.PullRequestBranch{Repo: &githubv39.Repository{FullName: githubv39.String("o/r")}},
		}
	}
	mine := pr("alice", "alice/r", "alice", true)
	for _, c := range []struct {
		name string
		rec  *recipe.Recipe
		it   githubItem
		pr   *githubv39.PullRequest
		ok   bool
	}{
		{"unset runs anywhere", recipeOn(), repo, nil, true},
		{"issue on issue", recipeOn("issue"), issue, nil, true},
		{"issue recipe on a PR", recipeOn("issue"), prItem, mine, false},
		{"issue recipe on a repo", recipeOn("issue"), repo, nil, false},
		{"pr on anyone's PR", recipeOn("pr"), prItem, pr("bob", "bob/r", "bob", true), true},
		{"pr on mine", recipeOn("pr"), prItem, mine, true},
		{"pr recipe on an issue", recipeOn("pr"), issue, nil, false},
		{"my-pr on mine", recipeOn("my-pr"), prItem, mine, true},
		{"my-pr on someone else's", recipeOn("my-pr"), prItem, pr("bob", "bob/r", "bob", true), false},
		{"my-pr on mine from a base branch", recipeOn("my-pr"), prItem, pr("alice", "o/r", "o", false), false},
		{"my-pr on mine from someone else's fork", recipeOn("my-pr"), prItem, pr("alice", "bob/r", "bob", true), false},
		{"repo on repo", recipeOn("repo"), repo, nil, true},
	} {
		if err := checkOn(c.rec, c.it, recipeTarget(c.it, c.pr, "alice")); (err == nil) != c.ok {
			t.Errorf("%s: checkOn = %v, want ok %v", c.name, err, c.ok)
		}
	}
}

// recipe list describes the built-ins as a board reads them: where each
// runs, its result, and its revises with their inputs.
func TestBuiltinRecipeInfos(t *testing.T) {
	infos, err := builtinRecipeInfos()
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]RecipeInfo{}
	for _, info := range infos {
		byName[info.Name] = info
	}
	for name, on := range map[string][]string{"triage": {"issue"}, "plan": {"issue"}, "fix": {"issue"}, "care": {"my-pr"}, "review": {"pr"}, "research": {"repo"}, "summarize": {"issue"}, "fanout": {"issue"}} {
		if got := byName[name]; !slices.Equal(got.On, on) || got.Kind == "" || got.Label == "" {
			t.Errorf("%s = %+v, want on %v, a kind and a label", name, got, on)
		}
	}
	fix := byName["fix"]
	if fix.Label != "Fix" || fix.Kind != "Change" {
		t.Errorf("fix = %+v", fix)
	}
	// fix iterates; looking after the PR is care's.
	if len(fix.Revises) != 1 || fix.Revises[0].ID != "iterate" || !slices.Equal(fix.Revises[0].Inputs, []string{"instruction"}) {
		t.Errorf("fix revises = %+v, want iterate alone, asking for instruction", fix.Revises)
	}
	var careRevises []string
	for _, rv := range byName["care"].Revises {
		careRevises = append(careRevises, rv.ID)
	}
	if care := byName["care"]; care.Kind != "Change" || strings.Join(careRevises, ",") != "address-comments,fix-ci,rebase,iterate" {
		t.Errorf("care = %+v, want a Change with address-comments, fix-ci, rebase, iterate", care)
	}
	for name, want := range map[string]string{"fix": "fix", "plan": "plan", "triage": "recipe-triage", "review": "recipe-review", "research": "research", "care": careTaskType} {
		if got := byName[name].TaskType; got != want {
			t.Errorf("%s's task type = %q, want %q", name, got, want)
		}
	}
	if byName["review"].Credentials != "clone" || fix.Credentials != "" {
		t.Errorf("credentials: review %q, fix %q; want clone and none", byName["review"].Credentials, fix.Credentials)
	}
	for _, in := range fix.Inputs {
		if in.Name == "instruction" && !in.Revise {
			t.Errorf("fix's instruction input is not marked revise: %+v", in)
		}
	}
}

// A Change recipe on a PR of yours (care) pushes from the PR: its head branch on
// the fork, leased at its head, from where it branched off its base.
func TestPRPushInputs(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/compare/main...alice:feature", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"merge_base_commit":{"sha":"b0"}}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	gh := githubv39.NewClient(nil)
	gh.BaseURL, _ = url.Parse(srv.URL + "/")

	pr := &githubv39.PullRequest{
		HTMLURL: githubv39.String("https://github.com/o/r/pull/9"),
		Title:   githubv39.String("t"),
		Body:    githubv39.String("b"),
		Head: &githubv39.PullRequestBranch{
			Ref: githubv39.String("feature"), SHA: githubv39.String("h1"), Label: githubv39.String("alice:feature"),
			Repo: &githubv39.Repository{FullName: githubv39.String("alice/r")},
		},
		Base: &githubv39.PullRequestBranch{Ref: githubv39.String("main")},
	}
	inputs := map[string]string{}
	if err := prPushInputs(context.Background(), gh, githubItem{Owner: "o", Repo: "r", Number: 9, IsPR: true}, pr, inputs); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"pushed_fork": "alice/r", "pushed_branch": "feature", "pushed_head": "h1", "pushed_base": "b0",
		"pushed_title": "t", "pushed_body": "b", "pr_url": "https://github.com/o/r/pull/9",
	}
	if fmt.Sprint(inputs) != fmt.Sprint(want) {
		t.Errorf("inputs = %v, want %v", inputs, want)
	}
}
