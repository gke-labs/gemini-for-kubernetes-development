package taskoutput

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	githubv39 "github.com/google/go-github/v39/github"
)

func notesDoc(t *testing.T, raw string) *Document {
	t.Helper()
	doc, err := Wrap("Notes", raw, Target{URL: "https://github.com/o/r"}, Source{Task: "recipe-research-2", Session: "recipe-research-1", Recipe: "research"})
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	return doc
}

func TestWrapNotes(t *testing.T) {
	data, err := Marshal(notesDoc(t, "```markdown\n# Deletes\nThey finalize.\n```"))
	if err != nil {
		t.Fatal(err)
	}
	docs, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	n, err := docs[0].NotesSpec()
	if err != nil {
		t.Fatal(err)
	}
	if n.Markdown != "# Deletes\nThey finalize." || n.Name != "" {
		t.Errorf("spec = %+v", n)
	}
	if got := docs[0].Offered(); len(got) != 3 || got[1].Verb != "push-notes" {
		t.Errorf("offered = %v", got)
	}
	if _, err := Wrap("Notes", "```\n```", Target{}, Source{}); err == nil {
		t.Error("empty notes were wrapped")
	}
}

func TestNotePath(t *testing.T) {
	doc := notesDoc(t, "x")
	for _, tc := range []struct {
		name, want string
	}{
		{"", "docs-exploration/research/recipe-research-1.md"},
		{"how-deletes-work", "docs-exploration/research/how-deletes-work.md"},
		{"how-deletes-work.md", "docs-exploration/research/how-deletes-work.md"},
		{"../escape", ""},
		{"a/b", ""},
		{".hidden", ""},
	} {
		got, err := NotePath(doc, &Notes{Name: tc.name})
		if tc.want == "" {
			if err == nil {
				t.Errorf("NotePath(%q) = %q, want an error", tc.name, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("NotePath(%q) = %q, %v; want %q", tc.name, got, err, tc.want)
		}
	}
}

// fakeGit is enough of GitHub's repository and git data API for push: one
// user, repositories by full name, each a map of branches to commits.
type fakeGit struct {
	mu      sync.Mutex
	login   string
	repos   map[string]*githubv39.Repository
	forks   int
	files   map[string]map[string]string // repo@branch → path → content
	heads   map[string]string            // repo@branch → commit sha
	parents map[string][]string          // commit sha → parents
	trees   map[string]map[string]string // tree sha → path → content
	commits map[string]string            // commit sha → tree sha
	n       int
	// race moves the branch once, between reading it and updating it.
	race bool
}

func newFakeGit(login string) *fakeGit {
	return &fakeGit{
		login:   login,
		repos:   map[string]*githubv39.Repository{"o/r": {Name: githubv39.String("r"), FullName: githubv39.String("o/r"), DefaultBranch: githubv39.String("main")}},
		files:   map[string]map[string]string{},
		heads:   map[string]string{"o/r@main": "c0"},
		parents: map[string][]string{},
		trees:   map[string]map[string]string{"t0": {}},
		commits: map[string]string{"c0": "t0"},
	}
}

func (f *fakeGit) sha(prefix string) string {
	f.n++
	return fmt.Sprintf("%s%d", prefix, f.n)
}

func (f *fakeGit) client(t *testing.T) *githubv39.Client {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.serve(w, r)
	}))
	t.Cleanup(srv.Close)
	gh := githubv39.NewClient(nil)
	gh.BaseURL, _ = url.Parse(srv.URL + "/")
	return gh
}

func (f *fakeGit) serve(w http.ResponseWriter, r *http.Request) {
	notFound := func() { w.WriteHeader(http.StatusNotFound); fmt.Fprint(w, `{"message":"Not Found"}`) }
	p := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if r.URL.Path == "/user" {
		fmt.Fprintf(w, `{"login":%q}`, f.login)
		return
	}
	if len(p) < 3 || p[0] != "repos" {
		notFound()
		return
	}
	full := p[1] + "/" + p[2]
	rest := strings.Join(p[3:], "/")
	repo, ok := f.repos[full]
	switch {
	case rest == "" && r.Method == http.MethodGet:
		if !ok {
			notFound()
			return
		}
		_ = json.NewEncoder(w).Encode(repo)
	case rest == "forks" && r.Method == http.MethodPost:
		f.forks++
		name := f.login + "/" + p[2]
		f.repos[name] = &githubv39.Repository{Name: githubv39.String(p[2]), FullName: githubv39.String(name), Fork: githubv39.Bool(true), Parent: &githubv39.Repository{FullName: githubv39.String(full)}, DefaultBranch: githubv39.String("main")}
		f.heads[name+"@main"] = f.heads[full+"@main"]
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, `{}`)
	case !ok:
		notFound()
	case strings.HasPrefix(rest, "git/ref/heads/") && r.Method == http.MethodGet:
		head, ok := f.heads[full+"@"+strings.TrimPrefix(rest, "git/ref/heads/")]
		if !ok {
			notFound()
			return
		}
		fmt.Fprintf(w, `{"ref":"refs/%s","object":{"sha":%q}}`, strings.TrimPrefix(rest, "git/ref/"), head)
	case strings.HasPrefix(rest, "git/commits/") && r.Method == http.MethodGet:
		sha := strings.TrimPrefix(rest, "git/commits/")
		fmt.Fprintf(w, `{"sha":%q,"tree":{"sha":%q}}`, sha, f.commits[sha])
	case strings.HasPrefix(rest, "contents/") && r.Method == http.MethodGet:
		ref := r.URL.Query().Get("ref")
		head, ok := f.heads[full+"@"+ref]
		content, has := f.trees[f.commits[head]][strings.TrimPrefix(rest, "contents/")]
		if !ok || !has {
			notFound()
			return
		}
		fmt.Fprintf(w, `{"type":"file","encoding":"base64","content":%q}`, base64.StdEncoding.EncodeToString([]byte(content)))
	case rest == "git/trees" && r.Method == http.MethodPost:
		var req struct {
			BaseTree string `json:"base_tree"`
			Tree     []struct{ Path, Content string }
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		tree := map[string]string{}
		for k, v := range f.trees[req.BaseTree] {
			tree[k] = v
		}
		for _, e := range req.Tree {
			tree[e.Path] = e.Content
		}
		sha := f.sha("t")
		f.trees[sha] = tree
		fmt.Fprintf(w, `{"sha":%q}`, sha)
	case rest == "git/commits" && r.Method == http.MethodPost:
		var req struct {
			Tree    string
			Parents []string
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		sha := f.sha("c")
		f.commits[sha] = req.Tree
		f.parents[sha] = req.Parents
		fmt.Fprintf(w, `{"sha":%q}`, sha)
	case rest == "git/refs" && r.Method == http.MethodPost:
		var req struct{ Ref, SHA string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		key := full + "@" + strings.TrimPrefix(req.Ref, "refs/heads/")
		if _, exists := f.heads[key]; exists {
			w.WriteHeader(http.StatusUnprocessableEntity)
			fmt.Fprint(w, `{"message":"Reference already exists"}`)
			return
		}
		f.heads[key] = req.SHA
		fmt.Fprint(w, `{}`)
	case strings.HasPrefix(rest, "git/refs/heads/") && r.Method == http.MethodPatch:
		var req struct{ SHA string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		key := full + "@" + strings.TrimPrefix(rest, "git/refs/heads/")
		if f.race {
			// Another save lands first.
			f.race = false
			other := f.sha("c")
			tree := map[string]string{"docs-exploration/research/other.md": "other\n"}
			for k, v := range f.trees[f.commits[f.heads[key]]] {
				tree[k] = v
			}
			f.trees["t-other"] = tree
			f.commits[other] = "t-other"
			f.parents[other] = []string{f.heads[key]}
			f.heads[key] = other
		}
		if f.parents[req.SHA][0] != f.heads[key] {
			w.WriteHeader(http.StatusUnprocessableEntity)
			fmt.Fprint(w, `{"message":"Update is not a fast forward"}`)
			return
		}
		f.heads[key] = req.SHA
		fmt.Fprint(w, `{}`)
	default:
		notFound()
	}
}

// note is what the branch holds at path.
func (f *fakeGit) note(repo, path string) (string, bool) {
	head, ok := f.heads[repo+"@research/notes"]
	if !ok {
		return "", false
	}
	c, ok := f.trees[f.commits[head]][path]
	return c, ok
}

func TestPushForksAndStartsTheBranch(t *testing.T) {
	f := newFakeGit("me")
	doc := notesDoc(t, "# Deletes\nThey finalize.")
	var out bytes.Buffer
	if err := ApplyAction(context.Background(), f.client(t), doc, "push-notes", false, &out); err != nil {
		t.Fatalf("push: %v\n%s", err, out.String())
	}
	if f.forks != 1 {
		t.Errorf("forked %d times", f.forks)
	}
	got, ok := f.note("me/r", "docs-exploration/research/recipe-research-1.md")
	if !ok || got != "# Deletes\nThey finalize.\n" {
		t.Errorf("the note on me/r = %q, %v", got, ok)
	}
	head := f.heads["me/r@research/notes"]
	if len(f.parents[head]) != 0 {
		t.Errorf("the branch's first commit has parents %v; want none", f.parents[head])
	}
	if _, ok := f.note("o/r", "docs-exploration/research/recipe-research-1.md"); ok {
		t.Error("pushed to the upstream")
	}

	// Again: nothing changes, no new commit.
	if err := ApplyAction(context.Background(), f.client(t), doc, "push-notes", false, &out); err != nil {
		t.Fatal(err)
	}
	if f.heads["me/r@research/notes"] != head || f.forks != 1 {
		t.Errorf("pushed again: head %s → %s, forks %d", head, f.heads["me/r@research/notes"], f.forks)
	}
	if !strings.Contains(out.String(), "No change") {
		t.Errorf("said:\n%s", out.String())
	}
}

func TestPushAddsToTheBranchAndReplaysARace(t *testing.T) {
	f := newFakeGit("me")
	gh := f.client(t)
	first := notesDoc(t, "# One")
	if err := ApplyAction(context.Background(), gh, first, "push-notes", false, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	second := notesDoc(t, "# Two")
	spec, _ := second.NotesSpec()
	spec.Name = "two"
	if err := second.Spec.Encode(spec); err != nil {
		t.Fatal(err)
	}
	f.race = true
	var out bytes.Buffer
	if err := ApplyAction(context.Background(), gh, second, "push-notes", false, &out); err != nil {
		t.Fatalf("push: %v\n%s", err, out.String())
	}
	for path, want := range map[string]string{
		"docs-exploration/research/recipe-research-1.md": "# One\n",
		"docs-exploration/research/other.md":             "other\n",
		"docs-exploration/research/two.md":               "# Two\n",
	} {
		if got, _ := f.note("me/r", path); got != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
}

func TestPushToOwnRepoAndRefusesAStranger(t *testing.T) {
	f := newFakeGit("o")
	if err := ApplyAction(context.Background(), f.client(t), notesDoc(t, "# Mine"), "push-notes", false, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if f.forks != 0 {
		t.Errorf("forked its own repository")
	}
	if got, _ := f.note("o/r", "docs-exploration/research/recipe-research-1.md"); got != "# Mine\n" {
		t.Errorf("the note on o/r = %q", got)
	}

	f = newFakeGit("me")
	f.repos["me/r"] = &githubv39.Repository{FullName: githubv39.String("me/r")}
	if err := ApplyAction(context.Background(), f.client(t), notesDoc(t, "# X"), "push-notes", false, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "not a fork of o/r") {
		t.Errorf("pushed to a repository that is not a fork: %v", err)
	}
}

func TestPushDryRunAndTargets(t *testing.T) {
	f := newFakeGit("me")
	var out bytes.Buffer
	if err := Apply(context.Background(), f.client(t), notesDoc(t, "# X"), true, &out); err != nil {
		t.Fatal(err)
	}
	if f.forks != 0 || len(f.heads) != 1 || !strings.Contains(out.String(), "Would push docs-exploration/research/recipe-research-1.md to research/notes on your fork of o/r") {
		t.Errorf("dry run: forks %d heads %v, said:\n%s", f.forks, f.heads, out.String())
	}
	doc := notesDoc(t, "# X")
	doc.Target.URL = "https://github.com/o/r/issues/1"
	if err := Apply(context.Background(), f.client(t), doc, false, &out); err == nil {
		t.Error("Notes were pushed for an issue target")
	}
}
