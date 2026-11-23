package taskoutput

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	githubv39 "github.com/google/go-github/v39/github"
)

// fakeIssues is o/r's issues: listed (open, by labels), created, commented.
type fakeIssues struct {
	mu       sync.Mutex
	issues   []map[string]any // number, title, body, labels, state
	comments map[int][]string
	listed   []string // the labels each list asked for
}

func (f *fakeIssues) client(t *testing.T) *githubv39.Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/issues", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Method == http.MethodPost {
			var req githubv39.IssueRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			n := 100 + len(f.issues)
			labels := []any{}
			if req.Labels != nil {
				for _, l := range *req.Labels {
					labels = append(labels, map[string]any{"name": l})
				}
			}
			f.issues = append(f.issues, map[string]any{"number": n, "title": req.GetTitle(), "body": req.GetBody(), "labels": labels, "state": "open"})
			fmt.Fprintf(w, `{"number":%d,"html_url":"https://github.com/o/r/issues/%d"}`, n, n)
			return
		}
		f.listed = append(f.listed, r.URL.Query().Get("labels"))
		want := map[string]bool{}
		for _, l := range strings.Split(r.URL.Query().Get("labels"), ",") {
			if l != "" {
				want[l] = true
			}
		}
		out := []map[string]any{}
		for _, is := range f.issues {
			if is["state"] != "open" {
				continue
			}
			has := map[string]bool{}
			for _, l := range is["labels"].([]any) {
				has[l.(map[string]any)["name"].(string)] = true
			}
			ok := true
			for l := range want {
				ok = ok && has[l]
			}
			if ok {
				out = append(out, is)
			}
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("/repos/o/r/issues/{n}/comments", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		n, _ := strconv.Atoi(r.PathValue("n"))
		if r.Method == http.MethodPost {
			var c githubv39.IssueComment
			_ = json.NewDecoder(r.Body).Decode(&c)
			if f.comments == nil {
				f.comments = map[int][]string{}
			}
			f.comments[n] = append(f.comments[n], c.GetBody())
			fmt.Fprint(w, `{"id":1}`)
			return
		}
		out := []map[string]any{}
		for _, b := range f.comments[n] {
			out = append(out, map[string]any{"body": b})
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	gh := githubv39.NewClient(nil)
	gh.BaseURL, _ = url.Parse(srv.URL + "/")
	return gh
}

const prURL = "https://github.com/o/r/pull/16291"

func issuesDoc(t *testing.T, raw, task string) *Document {
	t.Helper()
	doc, err := Wrap("Issues", raw, Target{URL: prURL}, Source{Task: task, Recipe: "file-validate-bug"})
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	return doc
}

const oneBug = `issues_output:
  issues:
    - key: DialogflowGeneratorEvaluation:create-lro-identity
      title: "DialogflowGeneratorEvaluation: identity lost when the create operation fails"
      body: |
        The create operation failed after the evaluation existed; every retry gets "has been taken".
      labels: [validate-kind/bug]
  note: One controller bug; the rest passed.
`

// An issue is filed once per key: the first apply opens it and says so on
// the PR; applying again files nothing and says nothing twice.
func TestOpenIssues(t *testing.T) {
	ctx := context.Background()
	f := &fakeIssues{}
	doc := issuesDoc(t, oneBug, "recipe-file-validate-bug-1")
	var out bytes.Buffer
	for range 2 {
		if err := ApplyAction(ctx, f.client(t), doc, "open-issues", false, &out); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.issues) != 1 {
		t.Fatalf("opened %d issues, want 1:\n%s", len(f.issues), out.String())
	}
	body := f.issues[0]["body"].(string)
	if !strings.Contains(body, "has been taken") || !strings.Contains(body, "Found by "+prURL) ||
		!strings.Contains(body, issueKeyMarker("DialogflowGeneratorEvaluation:create-lro-identity")) {
		t.Errorf("issue body = %q", body)
	}
	if f.listed[0] != "validate-kind/bug" {
		t.Errorf("listed open issues by %q, want the draft's labels", f.listed[0])
	}
	said := f.comments[16291]
	if len(said) != 1 || !strings.Contains(said[0], "#100 (DialogflowGeneratorEvaluation:create-lro-identity)") || !strings.Contains(said[0], "One controller bug") {
		t.Errorf("PR comments = %q", said)
	}
}

// The same bug found by another task comments on the open issue instead of
// opening a duplicate.
func TestOpenIssuesSeenAgain(t *testing.T) {
	ctx := context.Background()
	f := &fakeIssues{}
	var out bytes.Buffer
	if err := ApplyAction(ctx, f.client(t), issuesDoc(t, oneBug, "recipe-file-validate-bug-1"), "open-issues", false, &out); err != nil {
		t.Fatal(err)
	}
	if err := ApplyAction(ctx, f.client(t), issuesDoc(t, oneBug, "recipe-file-validate-bug-2"), "open-issues", false, &out); err != nil {
		t.Fatal(err)
	}
	if len(f.issues) != 1 {
		t.Fatalf("opened %d issues, want 1", len(f.issues))
	}
	if c := f.comments[100]; len(c) != 1 || !strings.HasPrefix(c[0], "Seen again.") {
		t.Errorf("comments on the issue = %q", c)
	}
	if c := f.comments[16291]; len(c) != 2 || !strings.Contains(c[1], "already open; seen again") {
		t.Errorf("PR comments = %q", c)
	}
}

// No issues is an answer: the note goes on the PR, nothing is filed.
func TestOpenIssuesNone(t *testing.T) {
	ctx := context.Background()
	f := &fakeIssues{}
	doc := issuesDoc(t, "issues_output:\n  issues: []\n  note: The only failure is the audit-log check; ces.googleapis.com writes no audit logs.\n", "recipe-file-validate-bug-3")
	var out bytes.Buffer
	if err := ApplyAction(ctx, f.client(t), doc, "open-issues", false, &out); err != nil {
		t.Fatal(err)
	}
	if len(f.issues) != 0 {
		t.Errorf("opened %d issues", len(f.issues))
	}
	if c := f.comments[16291]; len(c) != 1 || !strings.HasPrefix(c[0], "No issues filed.") || !strings.Contains(c[0], "writes no audit logs") {
		t.Errorf("PR comments = %q", c)
	}
}

// A dry run writes nothing.
func TestOpenIssuesDryRun(t *testing.T) {
	ctx := context.Background()
	f := &fakeIssues{}
	var out bytes.Buffer
	if err := ApplyAction(ctx, f.client(t), issuesDoc(t, oneBug, "recipe-file-validate-bug-4"), "open-issues", true, &out); err != nil {
		t.Fatal(err)
	}
	if len(f.issues) != 0 || len(f.comments) != 0 {
		t.Errorf("a dry run wrote: %d issues, %d comments", len(f.issues), len(f.comments))
	}
	if !strings.Contains(out.String(), "Would open an issue") && !strings.Contains(out.String(), "open an issue") {
		t.Errorf("dry run said:\n%s", out.String())
	}
}

func TestParseIssuesRefuses(t *testing.T) {
	for name, raw := range map[string]string{
		"no block":      "issues: []\n",
		"bad key":       "issues_output:\n  issues:\n    - key: \"has space\"\n      title: t\n      body: b\n",
		"no title":      "issues_output:\n  issues:\n    - key: k\n      body: b\n",
		"no body":       "issues_output:\n  issues:\n    - key: k\n      title: t\n",
		"duplicate key": "issues_output:\n  issues:\n    - {key: k, title: t, body: b}\n    - {key: k, title: t2, body: b2}\n",
	} {
		if _, err := Wrap("Issues", raw, Target{URL: prURL}, Source{Task: "x"}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	var many strings.Builder
	many.WriteString("issues_output:\n  issues:\n")
	for i := range maxIssues + 1 {
		fmt.Fprintf(&many, "    - {key: k%d, title: t, body: b}\n", i)
	}
	if _, err := Wrap("Issues", many.String(), Target{URL: prURL}, Source{Task: "x"}); err == nil {
		t.Errorf("%d issues accepted", maxIssues+1)
	}
}
