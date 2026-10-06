package taskoutput

import (
	"bytes"
	"context"
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

const reviewURL = "https://github.com/o/r/pull/5"

func reviewDoc(t *testing.T, raw, commit string) *Document {
	t.Helper()
	doc, err := Wrap("Review", raw, Target{URL: reviewURL, Commit: commit}, Source{Task: "recipe-review-1", Recipe: "review"})
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	return doc
}

const agentReview = "Here it is:\n```yaml\nreview:\n  body: |\n    Two problems.\n  comments:\n    - path: \" a.go\"\n      line: 3\n      side: \"right\\n\"\n      severity: high\n      body: off by one\n    - path: a.go\n      line: 99\n      body: not in the diff\n```"

func TestWrapReview(t *testing.T) {
	data, err := Marshal(reviewDoc(t, agentReview, "abc"))
	if err != nil {
		t.Fatal(err)
	}
	docs, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if docs[0].Target.Commit != "abc" {
		t.Errorf("target = %+v", docs[0].Target)
	}
	r, err := docs[0].ReviewSpec()
	if err != nil {
		t.Fatal(err)
	}
	c := r.Comments[0]
	if r.Body != "Two problems." || c.Path != "a.go" || c.Side != "RIGHT" || c.Severity != "HIGH" || r.Comments[1].Side != "RIGHT" {
		t.Errorf("spec = %+v", r)
	}
	if got := docs[0].Offered(); len(got) != 3 || got[1].Verb != "post-review" {
		t.Errorf("offered = %v", got)
	}
	for _, bad := range []string{"no review here", "review:\n  body: \"\"\n"} {
		if _, err := Wrap("Review", bad, Target{}, Source{}); err == nil {
			t.Errorf("%q was wrapped", bad)
		}
	}
}

// The diff GitHub has for a.go: lines 1-2 unchanged, 3 replaced, a second
// hunk adding line 11.
const aPatch = "@@ -1,3 +1,3 @@\n one\n two\n-three\n+THREE\n@@ -10,1 +10,2 @@\n ten\n+eleven"

func TestPlaceComments(t *testing.T) {
	diff := map[anchor]int{}
	patchLines(diff, "a.go", aPatch, 0)
	r := &Review{Body: "Sum.", Comments: []ReviewComment{
		{Path: "a.go", Line: 3, Side: "RIGHT", Body: "new three", Severity: "HIGH"},
		{Path: "a.go", Line: 3, Side: "LEFT", Body: "old three"},
		{Path: "a.go", Line: 11, Side: "RIGHT", StartLine: 10, StartSide: "RIGHT", Body: "range"},
		{Path: "a.go", Line: 11, Side: "RIGHT", StartLine: 2, StartSide: "RIGHT", Body: "across hunks"},
		{Path: "a.go", Line: 11, Side: "LEFT", Body: "no old 11"},
		{Path: "b.go", Line: 1, Side: "RIGHT", Body: "not in the PR"},
	}}
	body, comments, folded := placeComments(r, diff)
	if len(comments) != 3 || folded != 3 {
		t.Fatalf("placed %d, folded %d", len(comments), folded)
	}
	if got := comments[0].GetBody(); got != "**HIGH**: new three" {
		t.Errorf("body = %q", got)
	}
	if comments[2].GetStartLine() != 10 || comments[2].GetStartSide() != "RIGHT" {
		t.Errorf("range = %+v", comments[2])
	}
	for _, want := range []string{"Sum.", "`a.go:11`: across hunks", "`a.go:11`: no old 11", "`b.go:1`: not in the PR"} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
}

// fakeReviews is enough of GitHub for post-review: one open PR, its diff,
// and the caller's reviews on it.
type fakeReviews struct {
	mu      sync.Mutex
	head    string
	reviews []*githubv39.PullRequestReview
	created []githubv39.PullRequestReviewRequest
	deleted []int64
}

func (f *fakeReviews) client(t *testing.T) *githubv39.Client {
	mux := http.NewServeMux()
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"login":"me"}`)
	})
	mux.HandleFunc("/repos/o/r/pulls/5", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"state":"open","head":{"sha":%q},"base":{"ref":"main"}}`, f.head)
	})
	mux.HandleFunc("/repos/o/r/compare/", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/main...abc") {
			t.Errorf("compare %s, want main...abc", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"files": []map[string]string{{"filename": "a.go", "patch": aPatch}}})
	})
	mux.HandleFunc("/repos/o/r/pulls/5/reviews", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Method == http.MethodPost {
			var req githubv39.PullRequestReviewRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			f.created = append(f.created, req)
			fmt.Fprint(w, `{"id":99}`)
			return
		}
		_ = json.NewEncoder(w).Encode(f.reviews)
	})
	mux.HandleFunc("/repos/o/r/pulls/5/reviews/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		var id int64
		_, _ = fmt.Sscanf(strings.TrimPrefix(r.URL.Path, "/repos/o/r/pulls/5/reviews/"), "%d", &id)
		f.mu.Lock()
		f.deleted = append(f.deleted, id)
		f.mu.Unlock()
		fmt.Fprint(w, `{}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	gh := githubv39.NewClient(nil)
	gh.BaseURL, _ = url.Parse(srv.URL + "/")
	return gh
}

func review(id int64, login, state, body string) *githubv39.PullRequestReview {
	return &githubv39.PullRequestReview{ID: githubv39.Int64(id), State: githubv39.String(state), Body: githubv39.String(body), User: &githubv39.User{Login: githubv39.String(login)}}
}

func TestPostReview(t *testing.T) {
	ctx := context.Background()
	doc := reviewDoc(t, agentReview, "abc")

	t.Run("posts pending at the commit", func(t *testing.T) {
		f := &fakeReviews{head: "abc"}
		var out bytes.Buffer
		if err := ApplyAction(ctx, f.client(t), doc, "post-review", false, &out); err != nil {
			t.Fatal(err)
		}
		if len(f.created) != 1 {
			t.Fatalf("created %d reviews", len(f.created))
		}
		req := f.created[0]
		if req.Event != nil || req.GetCommitID() != "abc" || len(req.Comments) != 1 {
			t.Errorf("request = %+v", req)
		}
		if b := req.GetBody(); !strings.Contains(b, "`a.go:99`: not in the diff") || !strings.Contains(b, marker(doc)) {
			t.Errorf("body = %q", b)
		}
	})

	t.Run("replaces its own pending review", func(t *testing.T) {
		f := &fakeReviews{head: "def", reviews: []*githubv39.PullRequestReview{
			review(7, "me", "PENDING", "old"+"\n\n<!-- factory:task-output kind=Review task=recipe-review-0 -->"),
			review(8, "someone", "COMMENTED", "theirs"),
		}}
		var out bytes.Buffer
		if err := ApplyAction(ctx, f.client(t), doc, "post-review", false, &out); err != nil {
			t.Fatal(err)
		}
		if len(f.deleted) != 1 || f.deleted[0] != 7 || len(f.created) != 1 {
			t.Errorf("deleted %v, created %d", f.deleted, len(f.created))
		}
		if !strings.Contains(out.String(), "moved on") {
			t.Errorf("no word on the moved head:\n%s", out.String())
		}
	})

	t.Run("refuses a pending review it did not post", func(t *testing.T) {
		f := &fakeReviews{head: "abc", reviews: []*githubv39.PullRequestReview{review(7, "me", "PENDING", "my notes")}}
		err := ApplyAction(ctx, f.client(t), doc, "post-review", false, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "did not post") || len(f.deleted)+len(f.created) != 0 {
			t.Errorf("err = %v, deleted %v, created %d", err, f.deleted, len(f.created))
		}
	})

	t.Run("does not post a submitted review again", func(t *testing.T) {
		f := &fakeReviews{head: "abc", reviews: []*githubv39.PullRequestReview{review(7, "me", "COMMENTED", "sent"+marker(doc))}}
		var out bytes.Buffer
		if err := ApplyAction(ctx, f.client(t), doc, "post-review", false, &out); err != nil {
			t.Fatal(err)
		}
		if len(f.created) != 0 || !strings.Contains(out.String(), "already submitted") {
			t.Errorf("created %d:\n%s", len(f.created), out.String())
		}
	})

	t.Run("dry run writes nothing", func(t *testing.T) {
		f := &fakeReviews{head: "abc", reviews: []*githubv39.PullRequestReview{review(7, "me", "PENDING", marker(doc))}}
		var out bytes.Buffer
		if err := ApplyAction(ctx, f.client(t), doc, "post-review", true, &out); err != nil {
			t.Fatal(err)
		}
		if len(f.created)+len(f.deleted) != 0 || !strings.Contains(out.String(), "Would post") {
			t.Errorf("created %d, deleted %v:\n%s", len(f.created), f.deleted, out.String())
		}
	})

	t.Run("refuses without a commit", func(t *testing.T) {
		f := &fakeReviews{head: "abc"}
		err := ApplyAction(ctx, f.client(t), reviewDoc(t, agentReview, ""), "post-review", false, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "no commit") {
			t.Errorf("err = %v", err)
		}
	})
}
