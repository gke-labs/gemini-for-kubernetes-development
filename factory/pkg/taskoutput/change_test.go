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
	// A revise's may have no title, and keeps the PR's; a Change with
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
			prs = append(prs, &githubv39.PullRequest{HTMLURL: githubv39.String(u)})
		}
		_ = json.NewEncoder(w).Encode(prs)
	})
	mux.HandleFunc("/repos/o/r/issues/12/labels", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&f.labels)
		fmt.Fprint(w, `[]`)
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
	reactions     []string         // "<where> <content>"
}

func (f *fakeThreads) client(t *testing.T) *githubv39.Client {
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
	react := func(where string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			var req map[string]any
			_ = json.NewDecoder(r.Body).Decode(&req)
			if where == "review" {
				vars, _ := req["variables"].(map[string]any)
				where = fmt.Sprintf("review %v", vars["subjectId"])
				fmt.Fprint(w, `{"data":{"addReaction":{"reaction":{"content":"THUMBS_UP"}}}}`)
				f.reactions = append(f.reactions, fmt.Sprintf("%s %v", where, vars["content"]))
				return
			}
			f.reactions = append(f.reactions, fmt.Sprintf("%s %v", where, req["content"]))
			fmt.Fprint(w, `{}`)
		}
	}
	mux.HandleFunc("/repos/o/r/pulls/comments/100/reactions", react("review-comment 100"))
	mux.HandleFunc("/repos/o/r/issues/comments/200/reactions", react("comment 200"))
	mux.HandleFunc("/graphql", react("review"))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	gh := githubv39.NewClient(nil)
	gh.BaseURL, _ = url.Parse(srv.URL + "/")
	return gh
}

func reviseChange(t *testing.T, raw string) *Document {
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
		doc := reviseChange(t, answers)
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

	t.Run("marks the feedback it was handed resolved", func(t *testing.T) {
		// What the agent writes under feedback is not taken.
		doc := reviseChange(t, "change:\n  replies:\n    - inReplyTo: 100\n      body: Moved it.\n  feedback:\n    - kind: comment\n      id: 999\n")
		if c, _ := doc.ChangeSpec(); len(c.Feedback) != 0 {
			t.Fatalf("the agent's feedback was kept: %v", c.Feedback)
		}
		if err := doc.SetFeedback([]FeedbackRef{{Kind: "review-comment", ID: 100}, {Kind: "comment", ID: 200}, {Kind: "review", ID: 5, NodeID: "PRR_5"}}); err != nil {
			t.Fatal(err)
		}
		f := &fakeThreads{}
		var out bytes.Buffer
		if err := ApplyAction(ctx, f.client(t), doc, "post-replies", false, &out); err != nil {
			t.Fatalf("%v\n%s", err, out.String())
		}
		want := []string{"review-comment 100 +1", "comment 200 +1", "review PRR_5 THUMBS_UP"}
		if strings.Join(f.reactions, "|") != strings.Join(want, "|") {
			t.Errorf("reactions = %q, want %q", f.reactions, want)
		}
		// Feedback with nothing to say is still marked.
		bare := reviseChange(t, "change: {}\n")
		if err := bare.SetFeedback([]FeedbackRef{{Kind: "comment", ID: 200}}); err != nil {
			t.Fatal(err)
		}
		f = &fakeThreads{}
		if err := ApplyAction(ctx, f.client(t), bare, "post-replies", false, &out); err != nil {
			t.Fatal(err)
		}
		if len(f.reactions) != 1 || len(f.review)+len(f.issue) != 0 {
			t.Errorf("reactions %q, posted %d", f.reactions, len(f.review)+len(f.issue))
		}
	})

	t.Run("dry run posts nothing", func(t *testing.T) {
		f := &fakeThreads{}
		var out bytes.Buffer
		if err := ApplyAction(ctx, f.client(t), reviseChange(t, answers), "post-replies", true, &out); err != nil {
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

	t.Run("refused", func(t *testing.T) {
		for name, doc := range map[string]*Document{
			"another PR's comment": reviseChange(t, "change:\n  replies:\n    - inReplyTo: 300\n      body: x\n"),
			"no such comment":      reviseChange(t, "change:\n  replies:\n    - inReplyTo: 400\n      body: x\n"),
		} {
			f := &fakeThreads{}
			var out bytes.Buffer
			if err := ApplyAction(ctx, f.client(t), doc, "post-replies", false, &out); err == nil || len(f.review)+len(f.issue) != 0 {
				t.Errorf("%s: %v, posted %d", name, err, len(f.review)+len(f.issue))
			}
		}
		atIssue := reviseChange(t, answers)
		atIssue.Target.URL = issueURL
		var out bytes.Buffer
		if err := ApplyAction(ctx, (&fakeThreads{}).client(t), atIssue, "post-replies", false, &out); err == nil {
			t.Error("replies were posted with no PR")
		}
	})
}
