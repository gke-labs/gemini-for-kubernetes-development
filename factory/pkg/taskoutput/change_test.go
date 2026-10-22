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
	"testing"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
)

const issueURL = "https://github.com/o/r/issues/7"

// What an agent answers: prose, a fence, and a branch of its own, which
// the push's replaces.
const agentChange = "Here:\n```yaml\nchange:\n  title: \" r: fix the thing \"\n  branch: main\n  fork: someone/r\n  body: |\n    Does it.\n\n    Fixes #7\n  labels: [\" bug \", \"\"]\n```"

func changeDoc(t *testing.T) *Document {
	t.Helper()
	doc, err := Wrap("Change", agentChange, Target{URL: issueURL}, Source{Task: "fix-1", Recipe: "fix"})
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if err := doc.SetPushed(Pushed{Fork: "me/r", Branch: "issue-7-1", Base: "b0", Head: "h1"}); err != nil {
		t.Fatalf("SetPushed: %v", err)
	}
	return doc
}

func TestWrapChange(t *testing.T) {
	doc := changeDoc(t)
	if err := doc.AddLabels([]string{"factory", "bug"}); err != nil {
		t.Fatal(err)
	}
	data, err := Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	docs, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if docs[0].Target.Commit != "h1" {
		t.Errorf("target = %+v", docs[0].Target)
	}
	c, err := docs[0].ChangeSpec()
	if err != nil {
		t.Fatal(err)
	}
	if c.Title != "r: fix the thing" || c.Body != "Does it.\n\nFixes #7" || c.Fork != "me/r" || c.Branch != "issue-7-1" || c.Base != "b0" {
		t.Errorf("spec = %+v", c)
	}
	if strings.Join(c.Labels, ",") != "bug,factory" {
		t.Errorf("labels = %v", c.Labels)
	}
	if got := docs[0].Offered(); len(got) != 4 || got[1].Verb != "open-pr" || got[2].Verb != "post-replies" {
		t.Errorf("offered = %v", got)
	}

	// Without the push, the branch is nobody's: the agent's is dropped.
	raw, err := Wrap("Change", agentChange, Target{URL: issueURL}, Source{})
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := raw.ChangeSpec(); c.Fork != "" || c.Branch != "" {
		t.Errorf("the agent's branch was kept: %+v", c)
	}
	if err := raw.SetPushed(Pushed{Fork: "me/r"}); err == nil {
		t.Error("a push with no branch or head was accepted")
	}
	if _, err := Wrap("Change", "no change here", Target{}, Source{}); err == nil {
		t.Error("an answer with no change: block was wrapped")
	}
	// One on a PR may have no title, and keeps the PR's; a Change with
	// none at all is refused.
	untitled, err := Wrap("Change", "change:\n  title: \"\"\n  report: r\n", Target{}, Source{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := untitled.ChangeSpec(); err == nil {
		t.Error("a Change with no title was accepted")
	}
	if err := untitled.KeepTitle(" t0 ", "b0"); err != nil {
		t.Fatal(err)
	}
	if c, err := untitled.ChangeSpec(); err != nil || c.Title != "t0" || c.Body != "b0" || c.Report != "r" {
		t.Errorf("kept %+v, %v", c, err)
	}
}

// fakePRs is GitHub for open-pr: the caller is "me", o/r's default branch
// is main, and open lists the PRs open from a head.
type fakePRs struct {
	open    map[string]string // head → PR URL
	created []githubv39.NewPullRequest
	labels  []string
	// denyLabels answers a label write 403, as GitHub does a bot that
	// may not label the repository's issues.
	denyLabels bool
	// labelFailures answers that many label writes 502 first; labelCalls
	// counts the writes.
	labelFailures, labelCalls int
	// comments are o/r#5's, the PR an adopting Change adopts; closed is
	// whether it was closed.
	comments []string
	closed   bool
	// issueComments are o/r#7's, the issue a fix is for.
	issueComments []string
}

func (f *fakePRs) client(t *testing.T) *githubv39.Client {
	mux := http.NewServeMux()
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"login":"me"}`)
	})
	mux.HandleFunc("/repos/o/r", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"default_branch":"main"}`)
	})
	mux.HandleFunc("/repos/o/r/pulls", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var req githubv39.NewPullRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			f.created = append(f.created, req)
			fmt.Fprint(w, `{"number":12,"html_url":"https://github.com/o/r/pull/12"}`)
			return
		}
		if r.URL.Query().Get("state") != "open" {
			t.Errorf("listed PRs in state %q", r.URL.Query().Get("state"))
		}
		var prs []*githubv39.PullRequest
		if u, ok := f.open[r.URL.Query().Get("head")]; ok {
			n, _ := strconv.Atoi(u[strings.LastIndex(u, "/")+1:])
			prs = append(prs, &githubv39.PullRequest{HTMLURL: githubv39.String(u), Number: githubv39.Int(n)})
		}
		_ = json.NewEncoder(w).Encode(prs)
	})
	mux.HandleFunc("/repos/o/r/issues/12/labels", func(w http.ResponseWriter, r *http.Request) {
		f.labelCalls++
		if f.labelCalls <= f.labelFailures {
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprint(w, `{"message":"Server Error"}`)
			return
		}
		if f.denyLabels {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"message":"Must have admin rights to Repository."}`)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&f.labels)
		fmt.Fprint(w, `[]`)
	})
	mux.HandleFunc("/repos/o/r/issues/5/comments", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var c githubv39.IssueComment
			_ = json.NewDecoder(r.Body).Decode(&c)
			f.comments = append(f.comments, c.GetBody())
			fmt.Fprint(w, `{}`)
			return
		}
		var out []githubv39.IssueComment
		for _, b := range f.comments {
			out = append(out, githubv39.IssueComment{Body: githubv39.String(b)})
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("/repos/o/r/issues/7/comments", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var c githubv39.IssueComment
			_ = json.NewDecoder(r.Body).Decode(&c)
			f.issueComments = append(f.issueComments, c.GetBody())
			fmt.Fprint(w, `{}`)
			return
		}
		var out []githubv39.IssueComment
		for _, b := range f.issueComments {
			out = append(out, githubv39.IssueComment{Body: githubv39.String(b)})
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("/repos/o/r/pulls/5", func(w http.ResponseWriter, r *http.Request) {
		var pr githubv39.PullRequest
		_ = json.NewDecoder(r.Body).Decode(&pr)
		f.closed = pr.GetState() == "closed"
		fmt.Fprint(w, `{}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	gh := githubv39.NewClient(nil)
	gh.BaseURL, _ = url.Parse(srv.URL + "/")
	return gh
}

func TestOpenPR(t *testing.T) {
	ctx := context.Background()

	t.Run("opens a draft from the fork's branch", func(t *testing.T) {
		doc := changeDoc(t)
		f := &fakePRs{}
		var out bytes.Buffer
		if err := ApplyAction(ctx, f.client(t), doc, "open-pr", false, &out); err != nil {
			t.Fatal(err)
		}
		if len(f.created) != 1 {
			t.Fatalf("opened %d PRs", len(f.created))
		}
		req := f.created[0]
		if req.GetHead() != "me:issue-7-1" || req.GetBase() != "main" || !req.GetDraft() || req.GetTitle() != "r: fix the thing" {
			t.Errorf("request = %+v", req)
		}
		if b := req.GetBody(); !strings.HasPrefix(b, "Does it.\n\nFixes #7") || !strings.Contains(b, marker(doc)) {
			t.Errorf("body = %q", b)
		}
		if strings.Join(f.labels, ",") != "bug" {
			t.Errorf("labels = %v", f.labels)
		}
		if doc.Target.URL != "https://github.com/o/r/pull/12" {
			t.Errorf("target = %s, want the PR", doc.Target.URL)
		}
	})

	t.Run("draft: false opens it ready for review", func(t *testing.T) {
		doc := changeDoc(t)
		if err := doc.SetDraft(false); err != nil {
			t.Fatal(err)
		}
		f := &fakePRs{}
		var out bytes.Buffer
		if err := ApplyAction(ctx, f.client(t), doc, "open-pr", false, &out); err != nil {
			t.Fatal(err)
		}
		if len(f.created) != 1 || f.created[0].GetDraft() || !strings.Contains(out.String(), "Opening a PR on") {
			t.Errorf("opened %d, request %+v:\n%s", len(f.created), f.created, out.String())
		}
	})

	// An overseer opens its fix's PR as the bot that wrote it, which may
	// not label, and labels it as itself (WithLabeler).
	t.Run("the labeler labels what the opener may not", func(t *testing.T) {
		doc := changeDoc(t)
		opener, labeler := &fakePRs{denyLabels: true}, &fakePRs{}
		var out bytes.Buffer
		if err := ApplyAction(WithLabeler(ctx, labeler.client(t)), opener.client(t), doc, "open-pr", false, &out); err != nil {
			t.Fatal(err)
		}
		if len(opener.created) != 1 || len(opener.labels) != 0 || strings.Join(labeler.labels, ",") != "bug" || strings.Contains(out.String(), "Warning") {
			t.Errorf("opened %d, opener labels %v, labeler labels %v:\n%s", len(opener.created), opener.labels, labeler.labels, out.String())
		}
		// Without a labeler, the opener's 403 is a warning, not a failure.
		opener = &fakePRs{denyLabels: true}
		out.Reset()
		if err := ApplyAction(ctx, opener.client(t), changeDoc(t), "open-pr", false, &out); err != nil || !strings.Contains(out.String(), "Warning: could not label") {
			t.Errorf("no labeler: %v\n%s", err, out.String())
		}
	})

	// A label write GitHub failed on its side is tried again: a PR left
	// without its labels is one the watch's listings never find. One it
	// refused is not, and labels that never land are a warning.
	t.Run("labels are tried again on a 5xx, not on a 4xx", func(t *testing.T) {
		defer func(r []time.Duration) { labelRetries = r }(labelRetries)
		labelRetries = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}

		f := &fakePRs{labelFailures: 2}
		var out bytes.Buffer
		if err := ApplyAction(ctx, f.client(t), changeDoc(t), "open-pr", false, &out); err != nil {
			t.Fatal(err)
		}
		if f.labelCalls != 3 || strings.Join(f.labels, ",") != "bug" || strings.Contains(out.String(), "Warning") {
			t.Errorf("502 twice: %d calls, labels %v:\n%s", f.labelCalls, f.labels, out.String())
		}

		f = &fakePRs{denyLabels: true}
		out.Reset()
		if err := ApplyAction(ctx, f.client(t), changeDoc(t), "open-pr", false, &out); err != nil {
			t.Fatal(err)
		}
		if f.labelCalls != 1 || !strings.Contains(out.String(), "Warning: could not label") {
			t.Errorf("403: %d calls:\n%s", f.labelCalls, out.String())
		}

		f = &fakePRs{labelFailures: 99}
		out.Reset()
		if err := ApplyAction(ctx, f.client(t), changeDoc(t), "open-pr", false, &out); err != nil {
			t.Fatal(err)
		}
		if f.labelCalls != 4 || len(f.labels) != 0 || !strings.Contains(out.String(), "Warning: could not label") {
			t.Errorf("502 always: %d calls, labels %v:\n%s", f.labelCalls, f.labels, out.String())
		}
	})

	t.Run("applied again, an open PR gets its labels", func(t *testing.T) {
		doc := changeDoc(t)
		f := &fakePRs{open: map[string]string{"me:issue-7-1": "https://github.com/o/r/pull/12"}}
		var out bytes.Buffer
		if err := ApplyAction(ctx, f.client(t), doc, "open-pr", false, &out); err != nil {
			t.Fatal(err)
		}
		if len(f.created) != 0 || strings.Join(f.labels, ",") != "bug" {
			t.Errorf("opened %d, labels %v", len(f.created), f.labels)
		}
	})

	// An adopting Change (the adopt recipes): once its PR is open, the PR
	// it adopts gets a link to it, once, and is closed when asked.
	t.Run("adopts: links the original once, closes it when asked", func(t *testing.T) {
		doc := changeDoc(t)
		c, _ := doc.ChangeSpec()
		c.Adopts, c.CloseAdopted = "https://github.com/o/r/pull/5", true
		if err := doc.setSpec(c); err != nil {
			t.Fatal(err)
		}
		f := &fakePRs{}
		var out bytes.Buffer
		if err := ApplyAction(ctx, f.client(t), doc, "open-pr", false, &out); err != nil {
			t.Fatal(err)
		}
		if len(f.comments) != 1 || !strings.Contains(f.comments[0], "adopted here: https://github.com/o/r/pull/12, and closed.") || !f.closed {
			t.Errorf("comments %q, closed %v:\n%s", f.comments, f.closed, out.String())
		}
		// Applied again, at the open PR: no second comment.
		f.open = map[string]string{"me:issue-7-1": "https://github.com/o/r/pull/12"}
		if err := ApplyAction(ctx, f.client(t), doc, "open-pr", false, &out); err != nil || len(f.comments) != 1 {
			t.Errorf("again: %v, comments %q", err, f.comments)
		}
	})

	t.Run("an open PR is the one", func(t *testing.T) {
		doc := changeDoc(t)
		f := &fakePRs{open: map[string]string{"me:issue-7-1": "https://github.com/o/r/pull/9"}}
		var out bytes.Buffer
		if err := ApplyAction(ctx, f.client(t), doc, "open-pr", false, &out); err != nil {
			t.Fatal(err)
		}
		if len(f.created) != 0 || doc.Target.URL != "https://github.com/o/r/pull/9" {
			t.Errorf("opened %d, target %s", len(f.created), doc.Target.URL)
		}
		// Applied again, at the PR: still that one.
		if err := ApplyAction(ctx, f.client(t), doc, "open-pr", false, &out); err != nil || len(f.created) != 0 {
			t.Errorf("again: %v, opened %d", err, len(f.created))
		}
	})

	t.Run("dry run opens nothing", func(t *testing.T) {
		doc := changeDoc(t)
		f := &fakePRs{}
		var out bytes.Buffer
		if err := Apply(ctx, f.client(t), doc, true, &out); err != nil {
			t.Fatal(err)
		}
		if len(f.created) != 0 || doc.Target.URL != issueURL || !strings.Contains(out.String(), "Would open a draft PR") {
			t.Errorf("opened %d, target %s:\n%s", len(f.created), doc.Target.URL, out.String())
		}
	})

	t.Run("not the caller's fork", func(t *testing.T) {
		doc := changeDoc(t)
		_ = doc.SetPushed(Pushed{Fork: "someone/r", Branch: "issue-7-1", Head: "h1"})
		f := &fakePRs{}
		var out bytes.Buffer
		if err := ApplyAction(ctx, f.client(t), doc, "open-pr", false, &out); err == nil || len(f.created) != 0 {
			t.Errorf("opened from somebody else's fork: %v", err)
		}
	})

	t.Run("nothing pushed", func(t *testing.T) {
		doc, err := Wrap("Change", agentChange, Target{URL: issueURL}, Source{})
		if err != nil {
			t.Fatal(err)
		}
		f := &fakePRs{}
		var out bytes.Buffer
		if err := ApplyAction(ctx, f.client(t), doc, "open-pr", false, &out); err == nil || len(f.created) != 0 {
			t.Errorf("opened a PR with no branch: %v", err)
		}
	})
}

// fakeThreads is GitHub for post-replies on o/r#12: review comment 100 is
// on the PR, 300 on another; conversation comment 200 is on the PR.
type fakeThreads struct {
	review, issue []map[string]any // posted
}

func (f *fakeThreads) client(t *testing.T) *githubv39.Client {
	return serve(t, f.mux())
}

func serve(t *testing.T, mux *http.ServeMux) *githubv39.Client {
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	gh := githubv39.NewClient(nil)
	gh.BaseURL, _ = url.Parse(srv.URL + "/")
	return gh
}

func (f *fakeThreads) mux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/pulls/comments/100", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":100,"pull_request_url":"https://api.github.com/repos/o/r/pulls/12","html_url":"https://github.com/o/r/pull/12#discussion_r100"}`)
	})
	mux.HandleFunc("/repos/o/r/pulls/comments/300", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":300,"pull_request_url":"https://api.github.com/repos/o/r/pulls/13"}`)
	})
	mux.HandleFunc("/repos/o/r/pulls/comments/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	})
	mux.HandleFunc("/repos/o/r/issues/comments/200", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":200,"issue_url":"https://api.github.com/repos/o/r/issues/12","html_url":"https://github.com/o/r/pull/12#issuecomment-200","body":"Why not reuse ensureForkRemote?\nIt does this.","user":{"login":"rev"}}`)
	})
	mux.HandleFunc("/repos/o/r/pulls/12/reviews/500", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":500,"state":"COMMENTED","html_url":"https://github.com/o/r/pull/12#pullrequestreview-500","body":"Why did the HTTP logs not change?","user":{"login":"rev"}}`)
	})
	mux.HandleFunc("/repos/o/r/pulls/12/comments", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var req map[string]any
			_ = json.NewDecoder(r.Body).Decode(&req)
			f.review = append(f.review, req)
			fmt.Fprint(w, `{}`)
			return
		}
		_ = json.NewEncoder(w).Encode(f.review)
	})
	mux.HandleFunc("/repos/o/r/issues/12/comments", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var req map[string]any
			_ = json.NewDecoder(r.Body).Decode(&req)
			f.issue = append(f.issue, req)
			fmt.Fprint(w, `{}`)
			return
		}
		_ = json.NewEncoder(w).Encode(f.issue)
	})
	return mux
}

func prChange(t *testing.T, raw string) *Document {
	t.Helper()
	doc, err := Wrap("Change", raw, Target{URL: "https://github.com/o/r/pull/12"}, Source{Task: "fix-2", Recipe: "fix"})
	if err != nil {
		t.Fatal(err)
	}
	if err := doc.KeepTitle("t", "b"); err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestPostReplies(t *testing.T) {
	ctx := context.Background()
	const answers = "change:\n  replies:\n    - inReplyTo: 100\n      body: Moved it.\n    - inReplyTo: 200\n      body: Done, it does now.\n  report: All addressed.\n"

	t.Run("a thread reply, a quoted reply and the report, once", func(t *testing.T) {
		doc := prChange(t, answers)
		f := &fakeThreads{}
		var out bytes.Buffer
		if err := ApplyAction(ctx, f.client(t), doc, "post-replies", false, &out); err != nil {
			t.Fatal(err)
		}
		if len(f.review) != 1 || len(f.issue) != 2 {
			t.Fatalf("posted %d review and %d issue comments:\n%s", len(f.review), len(f.issue), out.String())
		}
		if r := f.review[0]; r["in_reply_to"] != float64(100) || !strings.HasPrefix(r["body"].(string), "Moved it.") || !strings.Contains(r["body"].(string), "task=fix-2 reply=100") {
			t.Errorf("thread reply = %v", r)
		}
		quoted := f.issue[0]["body"].(string)
		if !strings.HasPrefix(quoted, "> Why not reuse ensureForkRemote?\n> It does this.\n\n[In reply to @rev](https://github.com/o/r/pull/12#issuecomment-200)\n\nDone, it does now.") || !strings.Contains(quoted, "reply=200") {
			t.Errorf("quoted reply = %q", quoted)
		}
		if report := f.issue[1]["body"].(string); report != "All addressed."+marker(doc) {
			t.Errorf("report = %q", report)
		}
		// Applied again: everything is there already.
		if err := ApplyAction(ctx, f.client(t), doc, "post-replies", false, &out); err != nil {
			t.Fatal(err)
		}
		if len(f.review) != 1 || len(f.issue) != 2 {
			t.Errorf("posted again: %d review and %d issue comments", len(f.review), len(f.issue))
		}
	})

	t.Run("dry run posts nothing", func(t *testing.T) {
		f := &fakeThreads{}
		var out bytes.Buffer
		if err := ApplyAction(ctx, f.client(t), prChange(t, answers), "post-replies", true, &out); err != nil {
			t.Fatal(err)
		}
		if len(f.review)+len(f.issue) != 0 || !strings.Contains(out.String(), "Would reply") {
			t.Errorf("posted %d:\n%s", len(f.review)+len(f.issue), out.String())
		}
	})

	t.Run("a start's posts nothing", func(t *testing.T) {
		f := &fakeThreads{}
		var out bytes.Buffer
		// At the issue, with no replies: not an error.
		if err := ApplyAction(ctx, f.client(t), changeDoc(t), "post-replies", false, &out); err != nil {
			t.Fatal(err)
		}
		if len(f.review)+len(f.issue) != 0 {
			t.Errorf("posted %d", len(f.review)+len(f.issue))
		}
	})

	// A review's own id (the agent reads reviews beside the comments) has
	// no thread: it is answered with a comment quoting the review. An id
	// that is nothing on the PR is answered with a plain comment, and the
	// apply goes on. Both once.
	t.Run("a review and an unknown id are answered with comments, once", func(t *testing.T) {
		doc := prChange(t, "change:\n  replies:\n    - inReplyTo: 500\n      body: The logs were already aligned.\n    - inReplyTo: 400\n      body: Addressed.\n")
		f := &fakeThreads{}
		var out bytes.Buffer
		if err := ApplyAction(ctx, f.client(t), doc, "post-replies", false, &out); err != nil {
			t.Fatalf("%v\n%s", err, out.String())
		}
		if len(f.review) != 0 || len(f.issue) != 2 {
			t.Fatalf("posted %d review and %d issue comments:\n%s", len(f.review), len(f.issue), out.String())
		}
		quoted := f.issue[0]["body"].(string)
		if !strings.HasPrefix(quoted, "> Why did the HTTP logs not change?\n\n[In reply to @rev's review](https://github.com/o/r/pull/12#pullrequestreview-500)\n\nThe logs were already aligned.") || !strings.Contains(quoted, "reply=500") {
			t.Errorf("review reply = %q", quoted)
		}
		if plain := f.issue[1]["body"].(string); !strings.HasPrefix(plain, "Addressed.") || !strings.Contains(plain, "reply=400") {
			t.Errorf("plain reply = %q", plain)
		}
		if !strings.Contains(out.String(), "Warning: 400 is neither") {
			t.Errorf("no warning for the unknown id:\n%s", out.String())
		}
		if err := ApplyAction(ctx, f.client(t), doc, "post-replies", false, &out); err != nil {
			t.Fatal(err)
		}
		if len(f.issue) != 2 {
			t.Errorf("posted again: %d issue comments", len(f.issue))
		}
	})

	t.Run("refused", func(t *testing.T) {
		for name, doc := range map[string]*Document{
			"another PR's comment": prChange(t, "change:\n  replies:\n    - inReplyTo: 300\n      body: x\n"),
		} {
			f := &fakeThreads{}
			var out bytes.Buffer
			if err := ApplyAction(ctx, f.client(t), doc, "post-replies", false, &out); err == nil || len(f.review)+len(f.issue) != 0 {
				t.Errorf("%s: %v, posted %d", name, err, len(f.review)+len(f.issue))
			}
		}
		atIssue := prChange(t, answers)
		atIssue.Target.URL = issueURL
		var out bytes.Buffer
		if err := ApplyAction(ctx, (&fakeThreads{}).client(t), atIssue, "post-replies", false, &out); err == nil {
			t.Error("replies were posted with no PR")
		}
	})
}

// The title in the repository's own "area: subject" style, unquoted, as
// the agent wrote it on issue 908.
func TestWrapChangeUnquotedTitle(t *testing.T) {
	raw := "change:\n  title: repo-agent: make the cookie's Secure flag configurable #908\n  body: |\n    title: in the body stays as it is.\n\n    Fixes #908\n"
	doc, err := Wrap("Change", raw, Target{URL: issueURL}, Source{})
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	c, err := doc.ChangeSpec()
	if err != nil {
		t.Fatal(err)
	}
	if c.Title != "repo-agent: make the cookie's Secure flag configurable #908" || c.Body != "title: in the body stays as it is.\n\nFixes #908" {
		t.Errorf("spec = %+v", c)
	}
	// Quoted, block and empty titles are left alone.
	for _, in := range []string{"change:\n  title: \"a: b\"", "change:\n  title: 'a: b'", "change:\n  title: |", "change:\n  title:"} {
		if got := quotePlainTitle(in); got != in {
			t.Errorf("quotePlainTitle(%q) = %q", in, got)
		}
	}
}

// fakeChecks is GitHub for reruns on o/r#12, whose head is h2: run 1 of
// h2 failed, run 2 of h2 is running again, run 3 of h1 failed, run 4 of
// h2 passed.
type fakeChecks struct {
	fakeThreads
	reran []string
}

func (f *fakeChecks) client(t *testing.T) *githubv39.Client {
	mux := f.fakeThreads.mux()
	mux.HandleFunc("/repos/o/r/pulls/12", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"number":12,"head":{"sha":"h2"}}`)
	})
	runs := map[string]string{
		"1": `{"id":1,"name":"ci","head_sha":"h2","status":"completed","conclusion":"failure"}`,
		"2": `{"id":2,"name":"ci","head_sha":"h2","status":"in_progress"}`,
		"3": `{"id":3,"name":"ci","head_sha":"h1","status":"completed","conclusion":"failure"}`,
		"4": `{"id":4,"name":"ci","head_sha":"h2","status":"completed","conclusion":"success"}`,
	}
	mux.HandleFunc("/repos/o/r/actions/runs/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/repos/o/r/actions/runs/")
		if id, ok := strings.CutSuffix(rest, "/rerun-failed-jobs"); ok && r.Method == http.MethodPost {
			f.reran = append(f.reran, id)
			w.WriteHeader(http.StatusCreated)
			return
		}
		fmt.Fprint(w, runs[rest])
	})
	return serve(t, mux)
}

func TestPostRepliesReruns(t *testing.T) {
	ctx := context.Background()
	const reruns = "change:\n  report: A flake.\n  rerun:\n    - run: 1\n    - run: 2\n    - run: 3\n    - run: 4\n    - retest: /test pull-unit\n    - retest: /retest\n    - retest: /lgtm\n    - retest: \"/test a; rm\"\n"

	t.Run("failed runs of the head, Prow commands once", func(t *testing.T) {
		doc := prChange(t, reruns)
		f := &fakeChecks{}
		var out bytes.Buffer
		if err := ApplyAction(ctx, f.client(t), doc, "post-replies", false, &out); err != nil {
			t.Fatalf("%v\n%s", err, out.String())
		}
		if strings.Join(f.reran, ",") != "1" {
			t.Errorf("reran %v, want [1]:\n%s", f.reran, out.String())
		}
		var bodies []string
		for _, c := range f.issue {
			body, _, _ := strings.Cut(c["body"].(string), "\n")
			bodies = append(bodies, body)
		}
		if strings.Join(bodies, "|") != "A flake.|/test pull-unit|/retest" {
			t.Errorf("commented %q:\n%s", bodies, out.String())
		}
		if !strings.Contains(out.String(), `"/lgtm" is neither`) {
			t.Errorf("no warning for /lgtm:\n%s", out.String())
		}
		// Applied again: the comments are there; run 1 would be running
		// again on GitHub.
		f.reran = nil
		if err := ApplyAction(ctx, f.client(t), doc, "post-replies", false, &out); err != nil {
			t.Fatal(err)
		}
		if len(f.issue) != 3 {
			t.Errorf("commented again: %d comments", len(f.issue))
		}
	})

	t.Run("dry run reruns nothing", func(t *testing.T) {
		f := &fakeChecks{}
		var out bytes.Buffer
		if err := ApplyAction(ctx, f.client(t), prChange(t, reruns), "post-replies", true, &out); err != nil {
			t.Fatal(err)
		}
		if len(f.reran)+len(f.issue) != 0 || !strings.Contains(out.String(), "Would rerun the failed jobs of run 1") {
			t.Errorf("reran %v, commented %d:\n%s", f.reran, len(f.issue), out.String())
		}
	})
}

// fakePRText is GitHub for a PR's title and body: o/r#12, opened by
// author, as title/body; the applying account is me. Edits are recorded,
// and an issue comment (the report) records when it came.
type fakePRText struct {
	author, me, title, body string
	edits                   []map[string]any
	order                   []string
}

func (f *fakePRText) client(t *testing.T) *githubv39.Client {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/pulls/12", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			var req map[string]any
			_ = json.NewDecoder(r.Body).Decode(&req)
			f.edits = append(f.edits, req)
			f.order = append(f.order, "edit")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"number": 12, "title": f.title, "body": f.body, "user": map[string]any{"login": f.author}})
	})
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"login":%q}`, f.me)
	})
	mux.HandleFunc("/repos/o/r/issues/12/comments", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			f.order = append(f.order, "report")
			fmt.Fprint(w, `{}`)
			return
		}
		fmt.Fprint(w, `[]`)
	})
	return serve(t, mux)
}

// care-ci on KCC PR 14003 (2026-10-15): the fix was the PR description's
// release-note block. The agent wrote the new body; post-replies posted
// only its report, saying the description was updated, and the PR kept
// the old one. A rewrite now reaches the PR, before the report.
func TestPostRepliesRewritesThePR(t *testing.T) {
	ctx := context.Background()
	const (
		startBody = "Fixes #13935\n\n<!-- factory:task-output kind=Change task=recipe-fix-1 -->"
		newBody   = "Fixes #13935\n\n```release-note\nNONE\n```"
		answers   = "change:\n  body: |\n    Fixes #13935\n\n    ```release-note\n    NONE\n    ```\n  report: Added the release-note block.\n"
	)
	care := func(t *testing.T) *Document {
		t.Helper()
		doc, err := Wrap("Change", answers, Target{URL: "https://github.com/o/r/pull/12"}, Source{Task: "care-1", Recipe: "care-ci"})
		if err != nil {
			t.Fatal(err)
		}
		if err := doc.KeepTitle("fix: align", startBody); err != nil {
			t.Fatal(err)
		}
		return doc
	}

	t.Run("the body, keeping the factory marker, before the report", func(t *testing.T) {
		doc := care(t)
		if c, _ := doc.ChangeSpec(); len(c.Rewrites) != 1 || c.Rewrites[0] != "body" || c.Title != "fix: align" {
			t.Fatalf("change = %+v", c)
		}
		f := &fakePRText{author: "bot", me: "bot", title: "fix: align", body: startBody}
		var out bytes.Buffer
		if err := ApplyAction(ctx, f.client(t), doc, "post-replies", false, &out); err != nil {
			t.Fatal(err)
		}
		if len(f.edits) != 1 || f.edits[0]["body"] != newBody+"\n\n<!-- factory:task-output kind=Change task=recipe-fix-1 -->" || f.edits[0]["title"] != nil {
			t.Fatalf("edits = %v\n%s", f.edits, out.String())
		}
		if strings.Join(f.order, ",") != "edit,report" {
			t.Errorf("order = %v, want the rewrite before the report", f.order)
		}
	})

	t.Run("dry run edits nothing", func(t *testing.T) {
		f := &fakePRText{author: "bot", me: "bot", title: "fix: align", body: startBody}
		var out bytes.Buffer
		if err := ApplyAction(ctx, f.client(t), care(t), "post-replies", true, &out); err != nil {
			t.Fatal(err)
		}
		if len(f.edits) != 0 || !strings.Contains(out.String(), "Would rewrite the body") {
			t.Errorf("edits = %v\n%s", f.edits, out.String())
		}
	})

	t.Run("left alone", func(t *testing.T) {
		for name, f := range map[string]*fakePRText{
			"edited since the task started": {author: "bot", me: "bot", title: "fix: align", body: "Fixes #13935\n\nA human added this."},
			"somebody else's PR":            {author: "rev", me: "bot", title: "fix: align", body: startBody},
		} {
			var out bytes.Buffer
			if err := ApplyAction(ctx, f.client(t), care(t), "post-replies", false, &out); err != nil {
				t.Fatal(err)
			}
			if len(f.edits) != 0 || !strings.Contains(out.String(), "Not rewriting") || strings.Join(f.order, ",") != "report" {
				t.Errorf("%s: edits = %v, order = %v\n%s", name, f.edits, f.order, out.String())
			}
		}
	})

	t.Run("only what the agent rewrote", func(t *testing.T) {
		for raw, want := range map[string]string{
			"change:\n  report: x\n":                                                 "",
			"change:\n  body: |\n    Fixes #13935\n  report: x\n":                    "", // the same text, its marker aside
			"change:\n  title: 'fix: align CertificateManager'\n  report: x\n":       "title",
			"change:\n  title: 'fix: align'\n  body: Fixes #1 and #2\n  report: x\n": "body",
		} {
			doc, err := Wrap("Change", raw, Target{URL: "https://github.com/o/r/pull/12"}, Source{Task: "care-1"})
			if err != nil {
				t.Fatal(err)
			}
			if err := doc.KeepTitle("fix: align", startBody); err != nil {
				t.Fatal(err)
			}
			c, _ := doc.ChangeSpec()
			if got := strings.Join(c.Rewrites, ","); got != want {
				t.Errorf("%q: rewrites = %q, want %q", raw, got, want)
			}
		}
	})
}

// A PR body's release-note block keeps its closing fence (KCC #14121 lost
// it), and a care run that puts back a fence the PR's body lost is a
// rewrite of the body, not the same text.
func TestChangeBodyKeepsItsLastFence(t *testing.T) {
	raw := "change:\n  title: t\n  body: |\n    Fixes #1\n\n    ```release-note\n    NONE\n    ```\n"
	doc, err := Wrap("Change", raw, Target{URL: "https://github.com/o/r/pull/12"}, Source{Task: "care-1", Recipe: "care-ci"})
	if err != nil {
		t.Fatal(err)
	}
	broken := "Fixes #1\n\n```release-note\nNONE"
	if err := doc.KeepTitle("t", broken); err != nil {
		t.Fatal(err)
	}
	c, err := doc.changeSpec()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(c.Body, "```release-note\nNONE\n```") {
		t.Errorf("body lost its closing fence:\n%s", c.Body)
	}
	if len(c.Rewrites) != 1 || c.Rewrites[0] != "body" {
		t.Errorf("rewrites = %v, want [body]: the fence put back is a change", c.Rewrites)
	}
}

// A fix that should change nothing makes no commit and says why: the push
// records it unchanged, and open-pr posts the why on the issue, once, and
// opens no PR. No commit and no why is refused at the push.
func TestOpenPRUnchanged(t *testing.T) {
	ctx := context.Background()
	const why = "change:\n  comment: |\n    Already fixed by #6 on main; nothing to change.\n"
	doc, err := Wrap("Change", why, Target{URL: issueURL}, Source{Task: "fix-2", Recipe: "fix"})
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if err := doc.SetPushed(Pushed{Unchanged: true, Base: "b0", Head: "b0"}); err != nil {
		t.Fatalf("SetPushed: %v", err)
	}
	if err := doc.AddLabels([]string{"factory"}); err != nil {
		t.Fatal(err)
	}
	f := &fakePRs{}
	var out bytes.Buffer
	for range 2 {
		if err := ApplyAction(ctx, f.client(t), doc, "open-pr", false, &out); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.created) != 0 {
		t.Errorf("opened %d PRs for a fix that changed nothing", len(f.created))
	}
	if len(f.issueComments) != 1 || !strings.HasPrefix(f.issueComments[0], "Already fixed by #6 on main") || !strings.Contains(f.issueComments[0], marker(doc)) {
		t.Errorf("issue comments = %q, want the why once", f.issueComments)
	}
	if doc.Target.URL != issueURL {
		t.Errorf("target = %s, want the issue still", doc.Target.URL)
	}

	// No commit and no why: the push is refused, and the task fails.
	silent, err := Wrap("Change", agentChange, Target{URL: issueURL}, Source{Task: "fix-3"})
	if err != nil {
		t.Fatal(err)
	}
	if err := silent.SetPushed(Pushed{Unchanged: true, Base: "b0", Head: "b0"}); err == nil || !strings.Contains(err.Error(), "made no change") {
		t.Errorf("an unchanged push with no comment: %v", err)
	}
}
