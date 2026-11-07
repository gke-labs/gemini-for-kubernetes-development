package taskoutput

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	githubv39 "github.com/google/go-github/v39/github"
)

// groupPR is a fake GitHub with one open PR, #12 from me:issue-7-1, the
// group's: what its body became, and whether it was marked ready.
type groupPR struct {
	body    string
	draft   bool
	edits   int
	readied []string // node ids markPullRequestReadyForReview was called with
	labels  []string
	drafted []string // node ids convertPullRequestToDraft was called with
	removed []string // labels taken off
}

func (g *groupPR) client(t *testing.T) *githubv39.Client {
	t.Helper()
	pr := func() *githubv39.PullRequest {
		pr := &githubv39.PullRequest{Number: githubv39.Int(12), HTMLURL: githubv39.String("https://github.com/o/r/pull/12"),
			Body: githubv39.String(g.body), Draft: githubv39.Bool(g.draft), NodeID: githubv39.String("PR_12")}
		for _, l := range g.labels {
			pr.Labels = append(pr.Labels, &githubv39.Label{Name: githubv39.String(l)})
		}
		return pr
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"login":"me"}`) })
	mux.HandleFunc("/repos/o/r/pulls", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Query().Get("head") != "me:issue-7-1" {
			t.Errorf("unexpected %s %s", r.Method, r.URL)
		}
		_ = json.NewEncoder(w).Encode([]*githubv39.PullRequest{pr()})
	})
	mux.HandleFunc("/repos/o/r/pulls/12", func(w http.ResponseWriter, r *http.Request) {
		var edit githubv39.PullRequest
		_ = json.NewDecoder(r.Body).Decode(&edit)
		g.body, g.edits = edit.GetBody(), g.edits+1
		_ = json.NewEncoder(w).Encode(pr())
	})
	mux.HandleFunc("/repos/o/r/issues/12/labels", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `[]`) })
	mux.HandleFunc("/repos/o/r/issues/12/labels/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("unexpected %s %s", r.Method, r.URL)
		}
		name, _ := url.PathUnescape(strings.TrimPrefix(r.URL.EscapedPath(), "/repos/o/r/issues/12/labels/"))
		g.removed = append(g.removed, name)
		fmt.Fprint(w, `[]`)
	})
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var q struct {
			Query     string            `json:"query"`
			Variables map[string]string `json:"variables"`
		}
		_ = json.Unmarshal(b, &q)
		switch {
		case strings.Contains(q.Query, "markPullRequestReadyForReview"):
			g.readied, g.draft = append(g.readied, q.Variables["id"]), false
			fmt.Fprint(w, `{"data":{"markPullRequestReadyForReview":{"pullRequest":{"isDraft":false}}}}`)
		case strings.Contains(q.Query, "convertPullRequestToDraft"):
			g.drafted, g.draft = append(g.drafted, q.Variables["id"]), true
			fmt.Fprint(w, `{"data":{"convertPullRequestToDraft":{"pullRequest":{"isDraft":true}}}}`)
		default:
			t.Errorf("graphql query %q", q.Query)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	gh := githubv39.NewClient(nil)
	gh.BaseURL, _ = url.Parse(srv.URL + "/")
	return gh
}

// stepDoc is a grouped graph's step's Change for issue 8, pushed to the
// group's PR's branch: Continues #12, a draft unless the group's last.
func stepDoc(t *testing.T, continues int, last bool) *Document {
	t.Helper()
	doc, err := Wrap("Change", "```yaml\nchange:\n  title: \"r: mock it\"\n  body: |\n    Mocks it.\n\n    Fixes #8\n```", Target{URL: "https://github.com/o/r/issues/8"}, Source{Task: "fix-2", Recipe: "fix"})
	if err != nil {
		t.Fatal(err)
	}
	if err := doc.SetPushed(Pushed{Fork: "me/r", Branch: "issue-7-1", Base: "h1", Head: "h2"}); err != nil {
		t.Fatal(err)
	}
	if err := doc.SetContinues(continues); err != nil {
		t.Fatal(err)
	}
	if err := doc.SetDraft(!last); err != nil {
		t.Fatal(err)
	}
	return doc
}

// A grouped graph's step whose commits went to its group's open PR adds
// its section to the PR (which then fixes its issue too) once; the
// group's last marks the PR ready. A Change that continues no PR - a fix
// applied again - leaves the PR's body as it is.
func TestOpenPRContinuesTheGroupsPR(t *testing.T) {
	ctx := context.Background()
	const first = "Prepopulates it.\n\nFixes #7"

	t.Run("a step in the middle: its section, still a draft", func(t *testing.T) {
		g := &groupPR{body: first, draft: true}
		var out bytes.Buffer
		if err := ApplyAction(ctx, g.client(t), stepDoc(t, 12, false), "open-pr", false, &out); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(g.body, first+"\n\n---\n\n### r: mock it\n\nMocks it.\n\nFixes #8") || !strings.Contains(g.body, groupStepMarker(8)) {
			t.Errorf("body:\n%s", g.body)
		}
		if len(g.readied) != 0 || !g.draft {
			t.Errorf("a step in the middle marked the PR ready: %v", g.readied)
		}
		// Applied again: the section is there already.
		if err := ApplyAction(ctx, g.client(t), stepDoc(t, 12, false), "open-pr", false, &out); err != nil {
			t.Fatal(err)
		}
		if g.edits != 1 || strings.Count(g.body, "Fixes #8") != 1 {
			t.Errorf("applied twice: %d edits\n%s", g.edits, g.body)
		}
	})

	t.Run("a step in the middle on a PR handed to people: a draft again, unlabelled", func(t *testing.T) {
		// KCC #15000 / #15068: the graph was grouped once the first step's
		// PR was open, and the ready rule had handed it to people.
		g := &groupPR{body: first, draft: false, labels: []string{"overseer", "overseer/ready-for-human"}}
		var out bytes.Buffer
		if err := ApplyAction(ctx, g.client(t), stepDoc(t, 12, false), "open-pr", false, &out); err != nil {
			t.Fatal(err)
		}
		if strings.Join(g.drafted, ",") != "PR_12" || !g.draft || strings.Join(g.removed, ",") != "overseer/ready-for-human" || len(g.readied) != 0 {
			t.Errorf("drafted %v, removed %v, readied %v:\n%s", g.drafted, g.removed, g.readied, out.String())
		}
	})

	t.Run("the group's last on a PR that is not a draft: left ready", func(t *testing.T) {
		g := &groupPR{body: first, draft: false, labels: []string{"overseer/ready-for-human"}}
		var out bytes.Buffer
		if err := ApplyAction(ctx, g.client(t), stepDoc(t, 12, true), "open-pr", false, &out); err != nil {
			t.Fatal(err)
		}
		if len(g.drafted) != 0 || len(g.removed) != 0 || len(g.readied) != 0 {
			t.Errorf("drafted %v, removed %v, readied %v", g.drafted, g.removed, g.readied)
		}
	})

	t.Run("the group's last: its section, then ready for review", func(t *testing.T) {
		g := &groupPR{body: first, draft: true}
		var out bytes.Buffer
		if err := ApplyAction(ctx, g.client(t), stepDoc(t, 12, true), "open-pr", false, &out); err != nil {
			t.Fatal(err)
		}
		if g.edits != 1 || strings.Join(g.readied, ",") != "PR_12" || !strings.Contains(out.String(), "Marked https://github.com/o/r/pull/12 ready for review") {
			t.Errorf("edits %d, readied %v:\n%s", g.edits, g.readied, out.String())
		}
	})

	t.Run("the last applied again after marking ready failed: ready now", func(t *testing.T) {
		g := &groupPR{body: first + "\n\n" + groupStepMarker(8), draft: true}
		var out bytes.Buffer
		if err := ApplyAction(ctx, g.client(t), stepDoc(t, 12, true), "open-pr", false, &out); err != nil {
			t.Fatal(err)
		}
		if g.edits != 0 || len(g.readied) != 1 {
			t.Errorf("edits %d, readied %v", g.edits, g.readied)
		}
	})

	t.Run("a fix applied again, continuing no PR: the body is left alone", func(t *testing.T) {
		g := &groupPR{body: first, draft: true}
		var out bytes.Buffer
		if err := ApplyAction(ctx, g.client(t), stepDoc(t, 0, false), "open-pr", false, &out); err != nil {
			t.Fatal(err)
		}
		if g.edits != 0 || g.body != first || !strings.Contains(out.String(), "already has an open PR") {
			t.Errorf("edits %d, body %q:\n%s", g.edits, g.body, out.String())
		}
	})

	t.Run("dry run writes nothing", func(t *testing.T) {
		g := &groupPR{body: first, draft: true}
		var out bytes.Buffer
		if err := ApplyAction(ctx, g.client(t), stepDoc(t, 12, true), "open-pr", true, &out); err != nil {
			t.Fatal(err)
		}
		if g.edits != 0 || len(g.readied) != 0 {
			t.Errorf("dry run: edits %d, readied %v", g.edits, g.readied)
		}
	})
}

func TestIssueNumber(t *testing.T) {
	for u, want := range map[string]int{"https://github.com/o/r/issues/8": 8, "https://github.com/o/r/pull/8": 0, "https://github.com/o/r": 0} {
		if n, _ := issueNumber(u); n != want {
			t.Errorf("issueNumber(%s) = %d, want %d", u, n, want)
		}
	}
}
