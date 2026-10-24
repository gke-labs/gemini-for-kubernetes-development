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
	"testing"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
)

// fakeComments is an issue's comments, written by "watcher" (the client's
// own login) unless seeded otherwise.
type fakeComments struct {
	comments []*githubv39.IssueComment
	posts    int
	edits    int
}

func (f *fakeComments) client(t *testing.T) *githubv39.Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"login":"watcher"}`)
	})
	mux.HandleFunc("/repos/o/r/issues/12/comments", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var c githubv39.IssueComment
			_ = json.NewDecoder(r.Body).Decode(&c)
			f.posts++
			now := time.Now().Add(time.Duration(len(f.comments)) * time.Second)
			f.comments = append(f.comments, &githubv39.IssueComment{ID: githubv39.Int64(int64(100 + len(f.comments))), Body: c.Body, User: &githubv39.User{Login: githubv39.String("watcher")}, CreatedAt: &now})
			fmt.Fprint(w, "{}")
			return
		}
		_ = json.NewEncoder(w).Encode(f.comments)
	})
	mux.HandleFunc("/repos/o/r/issues/comments/", func(w http.ResponseWriter, r *http.Request) {
		var c githubv39.IssueComment
		_ = json.NewDecoder(r.Body).Decode(&c)
		for _, existing := range f.comments {
			if r.URL.Path == fmt.Sprintf("/repos/o/r/issues/comments/%d", existing.GetID()) {
				existing.Body = c.Body
				f.edits++
			}
		}
		fmt.Fprint(w, "{}")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	gh := githubv39.NewClient(nil)
	gh.BaseURL, _ = url.Parse(srv.URL + "/")
	return gh
}

func keyedPlan(t *testing.T, task, markdown string) *Document {
	t.Helper()
	doc := planDoc(t, task, markdown)
	doc.Comment = &CommentOptions{Key: "plan"}
	return doc
}

// A keyed comment is posted once, then edited by every later task's
// result, its version counted up; the same task's result applied again
// writes nothing. Another's comment with the key (a bot that left the
// pool, or anyone) is never edited: the writer's own follows it, numbered
// on.
func TestKeyedComment(t *testing.T) {
	now := time.Now().Add(-time.Hour)
	other := "**Implementation plan** · v2\n\nAn older plan.\n\n<!-- factory:comment key=plan v=2 -->"
	f := &fakeComments{comments: []*githubv39.IssueComment{{ID: githubv39.Int64(7), Body: &other, User: &githubv39.User{Login: githubv39.String("ada-coder-bot")}, CreatedAt: &now}}}
	gh := f.client(t)
	var out bytes.Buffer

	if err := ApplyAction(context.Background(), gh, keyedPlan(t, "recipe-plan-1", "## Summary\nFirst."), "comment", false, &out); err != nil {
		t.Fatal(err)
	}
	if f.posts != 1 || f.edits != 0 {
		t.Fatalf("first apply: %d posts, %d edits", f.posts, f.edits)
	}
	mine := f.comments[1]
	if b := mine.GetBody(); !strings.HasPrefix(b, "**Implementation plan** · v3 · updated ") || !strings.Contains(b, "First.") || !strings.Contains(b, "<!-- factory:comment key=plan v=3 -->") {
		t.Errorf("v3 = %q", b)
	}

	// Applied again: nothing written.
	if err := ApplyAction(context.Background(), gh, keyedPlan(t, "recipe-plan-1", "## Summary\nFirst."), "comment", false, &out); err != nil {
		t.Fatal(err)
	}
	if f.posts != 1 || f.edits != 0 {
		t.Errorf("re-apply: %d posts, %d edits", f.posts, f.edits)
	}

	// The next task's plan edits the writer's comment, as v4.
	if err := ApplyAction(context.Background(), gh, keyedPlan(t, "recipe-plan-2", "## Summary\nSecond."), "comment", false, &out); err != nil {
		t.Fatal(err)
	}
	if f.posts != 1 || f.edits != 1 {
		t.Fatalf("second task: %d posts, %d edits", f.posts, f.edits)
	}
	b := mine.GetBody()
	if !strings.HasPrefix(b, "**Implementation plan** · v4 · updated ") || !strings.Contains(b, "Second.") || strings.Contains(b, "First.") ||
		!strings.Contains(b, "key=plan v=4") || !strings.Contains(b, "task=recipe-plan-2") || strings.Contains(b, "task=recipe-plan-1") {
		t.Errorf("v4 = %q", b)
	}
	if f.comments[0].GetBody() != other {
		t.Error("another's comment was edited")
	}
}

// Without a key, a comment is a new one per task, as before.
func TestUnkeyedCommentIsNewPerTask(t *testing.T) {
	f := &fakeComments{}
	gh := f.client(t)
	var out bytes.Buffer
	for _, task := range []string{"recipe-plan-1", "recipe-plan-2"} {
		if err := ApplyAction(context.Background(), gh, planDoc(t, task, "## Summary\nX."), "comment", false, &out); err != nil {
			t.Fatal(err)
		}
	}
	if f.posts != 2 || f.edits != 0 {
		t.Errorf("%d posts, %d edits", f.posts, f.edits)
	}
}

func TestKeyedCommentFindsTheNewest(t *testing.T) {
	t0 := time.Now()
	body := func(v int) *string { s := fmt.Sprintf("x\n\n<!-- factory:comment key=plan v=%d -->", v); return &s }
	at := func(d time.Duration) *time.Time { t := t0.Add(d); return &t }
	comments := []*githubv39.IssueComment{
		{ID: githubv39.Int64(1), Body: body(1), User: &githubv39.User{Login: githubv39.String("ada-coder-bot")}, CreatedAt: at(0)},
		{ID: githubv39.Int64(2), Body: body(2), User: &githubv39.User{Login: githubv39.String("hopper-coder-bot")}, CreatedAt: at(time.Minute)},
		{ID: githubv39.Int64(3), Body: githubv39.String("<!-- factory:comment key=summary v=5 -->"), User: &githubv39.User{Login: githubv39.String("ada-coder-bot")}, CreatedAt: at(2 * time.Minute)},
	}
	if c, v := KeyedComment(comments, "", "plan"); c.GetID() != 2 || v != 2 {
		t.Errorf("anyone's: #%d v%d, want #2 v2", c.GetID(), v)
	}
	if c, v := KeyedComment(comments, "ADA-coder-bot", "plan"); c.GetID() != 1 || v != 1 {
		t.Errorf("ada's: #%d v%d, want #1 v1", c.GetID(), v)
	}
	if c, _ := KeyedComment(comments, "", "triage"); c != nil {
		t.Errorf("no triage comment, found #%d", c.GetID())
	}
}
