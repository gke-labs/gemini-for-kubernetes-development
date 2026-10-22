package commands

import (
	"context"
	"errors"
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
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/taskapi"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/taskoutput"
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
	if _, r, err := resolveRecipe(context.Background(), "triage", nil); err != nil || r.Name != "triage" {
		t.Errorf("built-in: %v, %v", r, err)
	}
	if _, _, err := resolveRecipe(context.Background(), "no-such-recipe", nil); err == nil {
		t.Error("unknown built-in loaded")
	}
	path := filepath.Join(t.TempDir(), "mine.yaml")
	if err := os.WriteFile(path, []byte("name: mine\nstart: {steps: [{run: 'true'}]}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, r, err := resolveRecipe(context.Background(), path, nil); err != nil || r.Name != "mine" {
		t.Errorf("file: %v, %v", r, err)
	}
}

// Every built-in recipe is a command of its own, `factory recipe <name>`,
// with a flag per input that shadows no other flag.
func TestBuiltinRecipeCommands(t *testing.T) {
	root := NewRootCommand(context.Background())
	recipeCmd, _, err := root.Find([]string{"recipe"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range recipe.BuiltinNames() {
		if name == "run" || name == "exec" || name == "list" {
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
	// REPO_OWNER and REPO_NAME are the repository the steps (pr-comments,
	// ci-logs) call GitHub about.
	if secrets["GITHUB_TOKEN"] != "tok" || env["ISSUE_NUMBER"] != "" || env["PR_NUMBER"] != "" || env["REPO_OWNER"] != "o" || env["REPO_NAME"] != "r" {
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
	data, _, err := recipe.Builtin("review")
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

func TestRecordedRun(t *testing.T) {
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

	// A run in a session records the recipe it ran, and the conversation
	// it continued.
	_, rec, err = recipe.Builtin("care-ci")
	if err != nil {
		t.Fatal(err)
	}
	run = recordedRun(spool.Task{ID: "t3", Session: "t1"}, rec, started)
	if run.Recipe != "care-ci" || run.Session != "t1" {
		t.Errorf("care-ci run = %+v", run)
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

func TestCheckApplicable(t *testing.T) {
	fanout := &recipe.Recipe{Name: "fanout", ApplicableWhen: &recipe.ApplicableWhen{Label: "{{ .Trigger }}/fanout"}}
	issue := githubItem{Owner: "o", Repo: "r", Number: 1}
	for _, c := range []struct {
		name   string
		rec    *recipe.Recipe
		it     githubItem
		labels []string
		ok     bool
	}{
		{"unset applies to anything", &recipe.Recipe{Name: "fix"}, issue, nil, true},
		{"labelled", fanout, issue, []string{"bug", "overseer/fanout"}, true},
		{"labelled in another case", fanout, issue, []string{"Overseer/Fanout"}, true},
		{"unlabelled", fanout, issue, []string{"bug"}, false},
		{"labelled for the trigger", fanout, issue, []string{"kcc/fanout"}, true},
		{"labelled for another trigger", fanout, issue, []string{"other/fanout"}, false},
		{"a repository", fanout, githubItem{Owner: "o", Repo: "r"}, nil, false},
	} {
		if err := checkApplicable(c.rec, c.it, c.labels, "kcc"); (err == nil) != c.ok {
			t.Errorf("%s: checkApplicable = %v, want ok %v", c.name, err, c.ok)
		}
	}
}

// recipe list describes the built-ins as a board reads them: where each
// runs and its result.
func TestBuiltinRecipeInfos(t *testing.T) {
	builtins, err := builtinCatalog()
	infos := mergeCatalog(builtins, nil, false)
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
	// Fan out is offered only on a fan-out parent.
	// With no factory config, the trigger is factory.
	if aw := byName["fanout"].ApplicableWhen; aw == nil || !slices.Equal(aw.Labels, []string{"overseer/fanout", "factory/fanout"}) {
		t.Errorf("fanout applicableWhen = %+v, want labels overseer/fanout, factory/fanout", aw)
	}
	fix := byName["fix"]
	if fix.Label != "Fix" || fix.Kind != "Change" || fix.OnFinish != "open-pr" || fix.Timeout != "3h" {
		t.Errorf("fix = %+v", fix)
	}
	if review := byName["review"]; review.OnFinish != "post-review" || review.Timeout != "45m" {
		t.Errorf("review = %+v, want on-finish post-review, timeout 45m", review)
	}
	if plan := byName["plan"]; plan.OnFinish != "" || plan.Timeout != "" {
		t.Errorf("plan = %+v, want nothing applied on finish", plan)
	}
	// care's jobs are recipes of session care, each a Change: the others
	// are its follow-ups.
	for name, label := range map[string]string{"care": "Care", "care-comments": "Address comments", "care-ci": "Fix CI", "care-rebase": "Rebase", "care-iterate": "Iterate"} {
		if got := byName[name]; got.Label != label || got.Kind != "Change" || got.Session != "care" || !slices.Equal(got.On, []string{"my-pr"}) {
			t.Errorf("%s = %+v, want %q, a Change in session care", name, got, label)
		}
	}
	if byName["fix"].Session != "" {
		t.Errorf("fix is in session %q, want none: care never continues it", byName["fix"].Session)
	}
	for name, want := range map[string]string{"fix": "fix", "plan": "plan", "triage": "recipe-triage", "review": "recipe-review", "research": "research", "care": "recipe-care", "care-ci": "recipe-care", "care-iterate": "recipe-care"} {
		if got := byName[name].TaskType; got != want {
			t.Errorf("%s's task type = %q, want %q", name, got, want)
		}
	}
	if byName["review"].Credentials != "clone" || fix.Credentials != "" {
		t.Errorf("credentials: review %q, fix %q; want clone and none", byName["review"].Credentials, fix.Credentials)
	}
	// Changing a fix once its PR is open is care-iterate's.
	for _, in := range fix.Inputs {
		if in.Name == "instruction" {
			t.Errorf("fix takes an instruction: %+v", in)
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

// A run in a session continues the conversation its group's recorded run
// is in, the one that run opened or continued, while it is the
// sandbox's; otherwise it opens one.
func TestContinuedSession(t *testing.T) {
	entries := []spool.Entry{{Task: spool.Task{ID: "care-3", Session: "care-1", Rotated: true}}, {Task: spool.Task{ID: "care-2"}}, {Task: spool.Task{ID: "care-1"}}}
	for _, tc := range []struct {
		why, recorded, want string
	}{
		{"none recorded", "", ""},
		{"the run opened it", `{"task":"care-1"}`, "care-1"},
		{"the run continued it", `{"task":"care-2","session":"care-1"}`, "care-1"},
		{"the run left it, full, for its own", `{"task":"care-3","session":"care-1"}`, "care-3"},
		{"the sandbox was recreated", `{"task":"care-0"}`, ""},
		{"unreadable", "{", ""},
	} {
		if got := continuedSession(tc.recorded, entries); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.why, got, tc.want)
		}
	}
	if factorysandbox.RunAnnotation("recipe-care") != "sandbox.gemini.google.com/recipe-care-run" {
		t.Errorf("session care records at %s, not where care's runs did", factorysandbox.RunAnnotation("recipe-care"))
	}
}

// mergeLabels: --pr-labels adds factory fix's labels to the labels input,
// each once, whatever its case.
func TestMergeLabels(t *testing.T) {
	if got := mergeLabels(" x, overseer ,", "overseer,Overseer/fanout,x,bug", ""); got != "x,overseer,Overseer/fanout,bug" {
		t.Errorf("mergeLabels = %q", got)
	}
	if got := mergeLabels("", ""); got != "" {
		t.Errorf("mergeLabels of nothing = %q", got)
	}
}

// fileSandbox serves task files from a map, for taskRecipe.
type fileSandbox struct {
	taskapi.Sandbox
	files map[string]string // "<task>/<name>"
}

func (f fileSandbox) ReadFile(_ context.Context, id, name string) ([]byte, error) {
	if s, ok := f.files[id+"/"+name]; ok {
		return []byte(s), nil
	}
	return nil, os.ErrNotExist
}

// A task's recipe is the one it kept, whatever it was; a task from before
// tasks kept it ran a built-in, read whole by its name, or failing that
// the sandbox's stripped copy. A task that ran no recipe has none.
func TestTaskRecipe(t *testing.T) {
	sb := fileSandbox{files: map[string]string{
		"a/" + spool.RecipeSourceFile: "name: mine\non: [repo]\nstart: {steps: [{run: x}]}\n",
		"b/" + spool.RecipeFile:       "name: plan\nstart: {steps: [{run: stripped}]}\n",
		"c/" + spool.RecipeFile:       "name: gone\nstart: {steps: [{run: stripped}]}\n",
	}}
	entry := func(id, name string) spool.Entry { return spool.Entry{Task: spool.Task{ID: id, Recipe: name}} }
	ctx := context.Background()
	if _, r, err := taskRecipe(ctx, sb, entry("a", "mine")); err != nil || r.Name != "mine" || !r.RunsOn(recipe.OnRepo) {
		t.Errorf("kept source: %+v, %v", r, err)
	}
	if _, r, err := taskRecipe(ctx, sb, entry("b", "plan")); err != nil || r.Name != "plan" || r.TaskOutput == nil {
		t.Errorf("a built-in's stripped copy: %+v, %v; want the built-in, whole", r, err)
	}
	if _, r, err := taskRecipe(ctx, sb, entry("c", "gone")); err != nil || r.Name != "gone" {
		t.Errorf("a stripped copy of no built-in: %+v, %v", r, err)
	}
	if _, r, err := taskRecipe(ctx, sb, entry("d", "triage")); err != nil || r.Name != "triage" {
		t.Errorf("nothing kept: %+v, %v; want the built-in", r, err)
	}
	if _, r, err := taskRecipe(ctx, sb, entry("e", "")); err != nil || r != nil {
		t.Errorf("no recipe: %+v, %v", r, err)
	}
}

// A name that is not a built-in's is the target repository's
// .agents/recipes/<name>.yaml; a built-in's name stays the built-in's,
// and a repository recipe may not take one, be named otherwise than its
// file, or declare a sandbox of its own.
func TestResolveRepoRecipe(t *testing.T) {
	files := map[string]string{
		".agents/recipes/explain-pr.yaml": "name: explain-pr\non: [pr]\nstart: {steps: [{ask: explain}]}\n",
		".agents/recipes/misnamed.yaml":   "name: other\nstart: {steps: [{run: x}]}\n",
		".agents/recipes/fix.yaml":        "name: fix\nstart: {steps: [{run: evil}]}\n",
		".agents/recipes/own.yaml":        "name: own\non: [repo]\nsandbox: {kind: warm}\nstart: {steps: [{run: x}]}\n",
		".agents/recipes/caring.yaml":     "name: caring\non: [my-pr]\nsession: care\nstart: {steps: [{ask: a}]}\n",
		".agents/recipes/sized.yaml":      "name: sized\non: [repo]\nsandbox: {prefix: tools, cpu-request: '8', cpu-limit: '8', memory-request: 8Gi, memory-limit: 8Gi, ephemeral-storage: 10Gi, disk: 100Gi}\nstart: {steps: [{ask: a}]}\n",
		".agents/recipes/revised.yaml":    "name: revised\nstart: {steps: [{ask: a, capture: out.md}]}\noutputs: [out.md]\ntask-output: {kind: Summary, from: out.md}\nrevise: [{id: again, label: Again, steps: [{ask: b, capture: out.md}]}]\n",
	}
	var read []string
	repo := func(_ context.Context, path string) ([]byte, string, error) {
		read = append(read, path)
		if s, ok := files[path]; ok {
			return []byte(s), "abc1234", nil
		}
		return nil, "", os.ErrNotExist
	}
	ctx := context.Background()
	if data, r, err := resolveRecipe(ctx, "explain-pr", repo); err != nil || r.Name != "explain-pr" || !r.RunsOn(recipe.OnPR) || string(data) != files[".agents/recipes/explain-pr.yaml"] || r.Source != ".agents/recipes/explain-pr.yaml@abc1234" {
		t.Errorf("repository recipe: %+v, %v", r, err)
	}
	// A repository's recipe names and sizes its own sandbox.
	if _, r, err := resolveRecipe(ctx, "sized", repo); err != nil || r.Sandbox == nil || r.Sandbox.CPURequest != "8" {
		t.Errorf("sized: %+v, %v", r, err)
	}
	read = nil
	if _, r, err := resolveRecipe(ctx, "fix", repo); err != nil || len(r.Start.Steps) < 3 || len(read) != 0 {
		t.Errorf("fix = %d steps, %v, read %v; want the built-in, the repository not asked", len(r.Start.Steps), err, read)
	}
	for name, want := range map[string]string{
		"misnamed": "not misnamed as its file is",
		"own":      "runs in a sandbox of its own",
		"revised":  "field revise not found",
		"caring":   "built-in group's",
		"missing":  "has no .agents/recipes/missing.yaml",
		"Bad_Name": "is not a recipe name",
	} {
		if _, _, err := resolveRecipe(ctx, name, repo); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", name, err, want)
		}
	}
	if _, err := parseRepoRecipe("fix", []byte(files[".agents/recipes/fix.yaml"])); err == nil || !strings.Contains(err.Error(), "built-in recipe's name") {
		t.Errorf("a repository fix: %v", err)
	}
}

// The operator's FACTORY_REPO_RECIPE_MODE: off reads nothing from the
// repository; augment is today's, at the commit (a built-in's name stays
// the built-in's); replace runs the repository's recipe of any name first,
// factory's when it has none.
func TestRepoRecipeModes(t *testing.T) {
	files := map[string]string{
		".agents/recipes/fix.yaml":        "name: fix\non: [issue]\nstart: {steps: [{run: kcc-fix}]}\n",
		".agents/recipes/explain-pr.yaml": "name: explain-pr\non: [pr]\nstart: {steps: [{ask: explain}]}\n",
		".agents/recipes/misnamed.yaml":   "name: other\nstart: {steps: [{run: x}]}\n",
	}
	var read []string
	repo := func(_ context.Context, path string) ([]byte, string, error) {
		read = append(read, path)
		if s, ok := files[path]; ok {
			return []byte(s), "abc1234", nil
		}
		return nil, "", os.ErrNotExist
	}
	ctx := context.Background()
	builtinFix := func(r *recipe.Recipe) bool { return len(r.Start.Steps) > 1 }

	t.Setenv(EnvRepoRecipeMode, "off")
	read = nil
	if _, r, err := resolveRecipe(ctx, "fix", repo); err != nil || !builtinFix(r) {
		t.Errorf("off: fix = %+v, %v", r, err)
	}
	if _, _, err := resolveRecipe(ctx, "explain-pr", repo); err == nil || len(read) != 0 {
		t.Errorf("off: explain-pr %v, read %v; want unknown, nothing read", err, read)
	}

	t.Setenv(EnvRepoRecipeMode, "augment")
	if _, r, err := resolveRecipe(ctx, "fix", repo); err != nil || !builtinFix(r) {
		t.Errorf("augment: fix = %+v, %v; want the built-in", r, err)
	}
	if _, r, err := resolveRecipe(ctx, "explain-pr", repo); err != nil || r.Name != "explain-pr" {
		t.Errorf("augment: explain-pr = %+v, %v", r, err)
	}

	t.Setenv(EnvRepoRecipeMode, "replace")
	if _, r, err := resolveRecipe(ctx, "fix", repo); err != nil || builtinFix(r) || r.Start.Steps[0].Run != "kcc-fix" {
		t.Errorf("replace: fix = %+v, %v; want the repository's", r, err)
	} else if r.Source != ".agents/recipes/fix.yaml@abc1234" {
		t.Errorf("replace: fix's source = %q, want the file at the commit read", r.Source)
	}
	if _, r, err := resolveRecipe(ctx, "review", repo); err != nil || r.Name != "review" || r.Source != recipe.SourceBuiltin {
		t.Errorf("replace: review (no repository file) = %+v, %v; want the built-in", r, err)
	}
	if _, _, err := resolveRecipe(ctx, "misnamed", repo); err == nil || !strings.Contains(err.Error(), "not misnamed as its file is") {
		t.Errorf("replace: misnamed %v", err)
	}
}

// A repository's files are read at FACTORY_REPO_RECIPE_COMMIT's commit
// when it names one; for latest or unset, at the default branch's head,
// resolved once to its SHA, which the read reports.
func TestRepoRecipeCommit(t *testing.T) {
	var refs []string
	heads := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/commits/") {
			heads++
			fmt.Fprint(w, "fedcba9876")
			return
		}
		refs = append(refs, r.URL.Query().Get("ref"))
		fmt.Fprint(w, `{"type":"file","encoding":"base64","content":"eDogMQ=="}`)
	}))
	defer srv.Close()
	gh := githubv39.NewClient(nil)
	gh.BaseURL, _ = url.Parse(srv.URL + "/")
	var revs []string
	for _, commit := range []string{"", "latest", "0123abc"} {
		t.Setenv(EnvRepoRecipeCommit, commit)
		read := githubRepoFile(gh, "o", "r")
		for range 2 {
			_, rev, err := read(context.Background(), ".agents/recipes/x.yaml")
			if err != nil {
				t.Fatal(err)
			}
			revs = append(revs, rev)
		}
	}
	if got := strings.Join(refs, ","); got != "fedcba9876,fedcba9876,fedcba9876,fedcba9876,0123abc,0123abc" {
		t.Errorf("refs = %q, want the resolved head for unset and latest, then 0123abc", got)
	}
	if got := strings.Join(revs, ","); got != "fedcba9876,fedcba9876,fedcba9876,fedcba9876,0123abc,0123abc" || heads != 2 {
		t.Errorf("revs = %q, %d head lookups; want one lookup per reader", got, heads)
	}
}

// A run that left a full conversation marks its task.json rotated and
// keeps everything else in it, the session it continued included.
func TestMarkSpoolRotated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, spool.TaskFile)
	if err := os.WriteFile(path, []byte(`{"id":"care-3","recipe":"care-ci","session":"care-1","output":{"kind":"Change"},"future":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := markSpoolRotated(dir); err != nil {
		t.Fatal(err)
	}
	task := readSpoolTask(dir)
	if !task.Rotated || task.Session != "care-1" || task.ID != "care-3" || task.Output == nil {
		t.Errorf("task = %+v", task)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), `"future":1`) {
		t.Errorf("a field factory does not know was dropped: %s", data)
	}
}

// writeSandbox keeps the task files written to it; failWrites refuses them.
type writeSandbox struct {
	fileSandbox
	failWrites bool
}

func (w *writeSandbox) WriteFile(_ context.Context, id, name string, data []byte) error {
	if w.failWrites {
		return errors.New("refused")
	}
	w.files[id+"/"+name] = string(data)
	return nil
}

// A failed apply is recorded in its task's directory, read back by task
// status, and emptied by a later apply that succeeds; one that cannot be
// recorded is only reported.
func TestApplyFailedMark(t *testing.T) {
	ctx := context.Background()
	sb := &writeSandbox{fileSandbox: fileSandbox{files: map[string]string{}}}
	if got := taskApplyFailure(ctx, sb, "t"); got != nil {
		t.Errorf("no mark: %+v", got)
	}
	markApplyFailed(ctx, sb, "t", "post-replies", errors.New("replying to comment 7: not found"))
	got := taskApplyFailure(ctx, sb, "t")
	if got == nil || got.Action != "post-replies" || got.Error != "replying to comment 7: not found" || got.At.IsZero() {
		t.Fatalf("after a failed apply: %+v", got)
	}
	clearApplyFailed(ctx, sb, "t")
	if got := taskApplyFailure(ctx, sb, "t"); got != nil {
		t.Errorf("after a successful apply: %+v", got)
	}
	if s, ok := sb.files["t/"+taskoutput.ApplyFailedFile]; !ok || s != "" {
		t.Errorf("mark = %q, %v; want emptied", s, ok)
	}
	clearApplyFailed(ctx, sb, "u")
	if _, ok := sb.files["u/"+taskoutput.ApplyFailedFile]; ok {
		t.Errorf("a task with no mark got one")
	}
	sb.failWrites = true
	markApplyFailed(ctx, sb, "v", "", errors.New("x")) // only reported
}

// The board's catalog follows the repository recipe mode: augment lists a
// repository's recipes after the built-ins, a built-in's name refused;
// replace puts a repository recipe of a built-in's name in its place and
// leaves warm and agent out; a repository's session group is held with
// the built-ins it joins.
func TestMergeCatalog(t *testing.T) {
	builtins, err := builtinCatalog()
	if err != nil {
		t.Fatal(err)
	}
	entry := func(name, y string, parse func(string, []byte) (*recipe.Recipe, error)) catalogEntry {
		t.Helper()
		path := ".agents/recipes/" + name + ".yaml"
		rec, err := parse(name, []byte(y))
		if err != nil {
			return catalogEntry{info: RecipeInfo{Name: name, Source: path, Error: err.Error()}}
		}
		info := recipeInfo(rec)
		info.Source = path
		return catalogEntry{info, rec}
	}
	source := func(infos []RecipeInfo) map[string]string {
		m := map[string]string{}
		for _, i := range infos {
			if i.Error != "" {
				m[i.Name+"!"] = i.Error
				continue
			}
			m[i.Name] = i.Source
		}
		return m
	}
	triage, _, _ := recipe.Builtin("triage")
	care, _, _ := recipe.Builtin("care-ci")
	warm, _, _ := recipe.Builtin("warm")
	own := "name: mine\non: [issue]\nstart: {steps: [{ask: a}]}\n"

	got := source(mergeCatalog(builtins, []catalogEntry{entry("triage", string(triage), parseRepoRecipe), entry("mine", own, parseRepoRecipe)}, false))
	if got["triage"] != "builtin" || got["mine"] != ".agents/recipes/mine.yaml" || !strings.Contains(got["triage!"], "built-in recipe's name") {
		t.Errorf("augment = %v", got)
	}

	replaced := mergeCatalog(builtins, []catalogEntry{entry("triage", string(triage), parseReplacingRecipe)}, true)
	if i := slices.IndexFunc(replaced, func(r RecipeInfo) bool { return r.Name == "triage" }); i < 0 || !replaced[i].Replaces {
		t.Errorf("a replacing triage is not marked so: %+v", replaced)
	}
	got = source(mergeCatalog(builtins, []catalogEntry{
		entry("triage", string(triage), parseReplacingRecipe),
		entry("care-ci", string(care), parseReplacingRecipe),
		entry("warm", string(warm), parseReplacingRecipe),
	}, true))
	if got["triage"] != ".agents/recipes/triage.yaml" || got["care-ci"] != ".agents/recipes/care-ci.yaml" || got["care"] != "builtin" {
		t.Errorf("replace = %v", got)
	}
	if _, ok := got["warm"]; ok {
		t.Errorf("replace lists warm: %v", got)
	}

	// care-ci on PRs rather than my PRs breaks the care group: the
	// built-in comes back, the repository's is not a recipe.
	broken := strings.Replace(string(care), "\non: [my-pr]\n", "\non: [pr]\n", 1)
	got = source(mergeCatalog(builtins, []catalogEntry{entry("care-ci", broken, parseReplacingRecipe)}, true))
	if got["care-ci"] != "builtin" || !strings.Contains(got["care-ci!"], "session care") {
		t.Errorf("a broken group = %v", got)
	}
	// So does a repository's own group without its default.
	got = source(mergeCatalog(builtins, []catalogEntry{entry("deploy-check", "name: deploy-check\non: [issue]\nsession: deploy\nstart: {steps: [{ask: a}]}\n", parseRepoRecipe)}, false))
	if _, ok := got["deploy-check"]; ok || !strings.Contains(got["deploy-check!"], "no recipe is named deploy") {
		t.Errorf("a repository group = %v", got)
	}
}
