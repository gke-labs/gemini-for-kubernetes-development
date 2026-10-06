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
	if got := docs[0].Offered(); len(got) != 3 || got[1].Verb != "open-pr" {
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
	for _, bad := range []string{"no change here", "change:\n  title: \"\"\n  body: x\n"} {
		if _, err := Wrap("Change", bad, Target{}, Source{}); err == nil {
			t.Errorf("%q was wrapped", bad)
		}
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
