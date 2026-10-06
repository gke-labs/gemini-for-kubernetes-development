package commands

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	githubv39 "github.com/google/go-github/v39/github"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/feedback"
	factorysandbox "github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/sandbox"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/spool"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/taskoutput"
)

// The watch revises a PR's fix run only in the sandbox aliased to that
// PR that records one.
func TestFixRunOf(t *testing.T) {
	sb := func(name, htmlURL string, fixRun bool) unstructured.Unstructured {
		var u unstructured.Unstructured
		u.SetName(name)
		a := map[string]string{"htmlURL": htmlURL}
		if fixRun {
			a[factorysandbox.RunAnnotation("fix")] = `{"task":"fix-1"}`
		}
		u.SetAnnotations(a)
		return u
	}
	pr := "https://github.com/o/r/pull/12"
	for name, c := range map[string]struct {
		items []unstructured.Unstructured
		want  string
	}{
		"a fix run":            {[]unstructured.Unstructured{sb("factory-pr-r-12", pr, false), sb("fix-r-7", "https://github.com/O/r/pull/12/", true)}, "fix-r-7"},
		"no fix run":           {[]unstructured.Unstructured{sb("fix-r-7", pr, false)}, ""},
		"another repo's PR 12": {[]unstructured.Unstructured{sb("fix-x-7", "https://github.com/o/x/pull/12", true)}, ""},
	} {
		if got := fixRunOf(c.items, pr); got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
	if !factoryPosted("Done.\n\n<!-- factory:task-output kind=Change task=fix-2 reply=1 -->") || factoryPosted("please fix") {
		t.Error("factoryPosted")
	}
}

// A fix's revise gets the push its session last made, and that task's
// title and body, skipping tasks that pushed nothing and other sessions.
func TestChangeInputs(t *testing.T) {
	files := map[string]string{
		"fix-1/" + taskoutput.PushedFile: `{"fork":"me/r","branch":"issue-7-1","base":"b0","head":"h1"}`,
		"fix-1/" + taskoutput.File:       "apiVersion: " + taskoutput.APIVersion + "\nkind: Change\ntarget: {url: https://github.com/o/r/issues/7}\nspec: {title: t1, body: b1}\n",
		"fix-2/" + taskoutput.PushedFile: `{"fork":"me/r","branch":"issue-7-1","base":"b0","head":"h2"}`,
		"other/" + taskoutput.PushedFile: `{"fork":"me/r","branch":"x","base":"b0","head":"hx"}`,
	}
	read := func(id, name string) ([]byte, error) {
		if v, ok := files[id+"/"+name]; ok {
			return []byte(v), nil
		}
		return nil, os.ErrNotExist
	}
	entries := []spool.Entry{
		{Task: spool.Task{ID: "other", Recipe: "fix"}},
		{Task: spool.Task{ID: "fix-3", Recipe: "fix", Session: "fix-1"}}, // failed before its push
		{Task: spool.Task{ID: "fix-2", Recipe: "fix", Session: "fix-1"}},
		{Task: spool.Task{ID: "fix-1", Recipe: "fix"}},
	}
	inputs := map[string]string{}
	if err := changeInputs(inputs, entries, "fix-1", "fix", read); err != nil {
		t.Fatal(err)
	}
	if inputs["pushed_branch"] != "issue-7-1" || inputs["pushed_head"] != "h2" || inputs["pushed_fork"] != "me/r" || inputs["pushed_base"] != "b0" {
		t.Errorf("inputs = %v", inputs)
	}
	// fix-2 left no task output: no title to keep.
	if inputs["pushed_title"] != "" {
		t.Errorf("title = %q", inputs["pushed_title"])
	}
	delete(files, "fix-2/"+taskoutput.PushedFile)
	if err := changeInputs(inputs, entries, "fix-1", "fix", read); err != nil {
		t.Fatal(err)
	}
	if inputs["pushed_head"] != "h1" || inputs["pushed_title"] != "t1" || inputs["pushed_body"] != "b1" {
		t.Errorf("inputs = %v", inputs)
	}
	if err := changeInputs(map[string]string{}, entries, "fix-9", "fix", read); err == nil {
		t.Error("a session that pushed nothing was revised")
	}
	if err := changeInputs(map[string]string{}, entries[:1], "other", "fix", func(string, string) ([]byte, error) { return []byte(`{"branch":""}`), nil }); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Errorf("a push with no branch: %v", err)
	}

	it := githubItem{Owner: "o", Repo: "r", Number: 7}
	for want, a := range map[string]map[string]string{
		"https://github.com/o/r/pull/12": {"htmlURL": "https://github.com/o/r/pull/12"},
		"https://github.com/o/r/pull/13": {"htmlURL": "https://github.com/o/r/issues/7", "pr": "13"},
		"":                               {"htmlURL": "https://github.com/o/r/issues/7"},
	} {
		if got := prURLOf(a, it); got != want {
			t.Errorf("prURLOf(%v) = %q, want %q", a, got, want)
		}
	}
}

// On a fix's PR the member's own words are feedback, whoever authored the
// PR; what factory posted from a task output, and bots, are not.
func TestFixFeedbackPolicy(t *testing.T) {
	u := func(login string) *githubv39.User { return &githubv39.User{Login: githubv39.String(login)} }
	at := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	c := func(id int64, login, body string) *githubv39.IssueComment {
		return &githubv39.IssueComment{ID: githubv39.Int64(id), User: u(login), CreatedAt: &at, Body: githubv39.String(body)}
	}
	h := &feedback.History{Comments: []*githubv39.IssueComment{
		c(1, "member", "rename foo"),
		c(2, "member", "Done.\n\n<!-- factory:task-output fix-1 -->"),
		c(3, "gemini-code-assist[bot]", "consider bar"),
		c(4, "alice", "and baz"),
	}}
	pr := &githubv39.PullRequest{User: u("member")}
	var got []int64
	for _, it := range feedback.Pending(t.Context(), pr, h, fixFeedbackPolicy, nil, time.Time{}, time.Time{}) {
		got = append(got, it.ID)
	}
	if len(got) != 2 || got[0] != 1 || got[1] != 4 {
		t.Errorf("pending = %v, want [1 4]", got)
	}
}

// The feedback input cuts long bodies and holds only what fits; what does
// not stays for the next round.
func TestFeedbackInput(t *testing.T) {
	if in, handed := feedbackInput(nil); in != "" || handed != nil {
		t.Errorf("no feedback = %q, %v", in, handed)
	}
	long := strings.Repeat("é", maxFeedbackBody+10)
	in, handed := feedbackInput([]feedback.Item{{Kind: feedback.KindComment, ID: 1, Body: long}})
	var items []feedback.Item
	if err := json.Unmarshal([]byte(in), &items); err != nil {
		t.Fatal(err)
	}
	if len(handed) != 1 || len(items) != 1 || len([]rune(items[0].Body)) != maxFeedbackBody+1 {
		t.Errorf("a long body: %d handed, %d in the input of %d runes", len(handed), len(items), len([]rune(items[0].Body)))
	}

	var many []feedback.Item
	for i := range 100 {
		many = append(many, feedback.Item{Kind: feedback.KindReviewComment, ID: int64(i), Body: long})
	}
	in, handed = feedbackInput(many)
	if len(in) > maxFeedbackInput || len(handed) == 0 || len(handed) == len(many) {
		t.Errorf("too much feedback: %d bytes, %d of %d handed", len(in), len(handed), len(many))
	}
	if err := json.Unmarshal([]byte(in), &items); err != nil || len(items) != len(handed) || items[0].ID != 0 {
		t.Errorf("the input holds %d items (%v), %d handed", len(items), err, len(handed))
	}
}

// The watch's check on a fix's PR: everything the member and reviewers
// said, whenever they said it, less what carries the member's 👀 or 👍 —
// read through GitHub as pr watch does.
func TestPendingFixFeedback(t *testing.T) {
	mux := http.NewServeMux()
	reply := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, body) }
	}
	mux.HandleFunc("/user", reply(`{"login":"member"}`))
	// The last commit (a Fix CI, say) is after every comment: it answers none.
	mux.HandleFunc("/repos/o/r/pulls/12/commits", reply(`[{"commit":{"committer":{"date":"2026-10-06T12:00:00Z"}}}]`))
	mux.HandleFunc("/repos/o/r/issues/12/comments", reply(`[
		{"id":1,"user":{"login":"member"},"created_at":"2026-10-06T10:00:00Z","body":"rename foo"},
		{"id":2,"user":{"login":"member"},"created_at":"2026-10-06T10:01:00Z","body":"handed already"},
		{"id":3,"user":{"login":"member"},"created_at":"2026-10-06T10:02:00Z","body":"Done.\n<!-- factory:task-output fix-1 -->"}]`))
	mux.HandleFunc("/repos/o/r/pulls/12/reviews", reply(`[
		{"id":10,"node_id":"PRR_10","user":{"login":"alice"},"submitted_at":"2026-10-06T11:00:00Z","state":"COMMENTED","body":"addressed already"}]`))
	mux.HandleFunc("/repos/o/r/pulls/12/comments", reply(`[
		{"id":100,"pull_request_review_id":10,"user":{"login":"alice"},"created_at":"2026-10-06T11:00:00Z","path":"a.go","line":3,"body":"nit"}]`))
	mux.HandleFunc("/repos/o/r/issues/comments/1/reactions", reply(`[{"content":"eyes","user":{"login":"alice"}}]`))
	mux.HandleFunc("/repos/o/r/issues/comments/2/reactions", reply(`[{"content":"eyes","user":{"login":"member"}}]`))
	mux.HandleFunc("/repos/o/r/pulls/comments/100/reactions", reply(`[]`))
	mux.HandleFunc("/graphql", reply(`{"data":{"node":{"reactions":{"nodes":[{"content":"EYES","user":{"login":"member"}},{"content":"THUMBS_UP","user":{"login":"member"}}]}}}}`))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	gh := githubv39.NewClient(nil)
	gh.BaseURL, _ = url.Parse(srv.URL + "/")

	items, err := pendingFixFeedback(t.Context(), gh, "o", "r", 12)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, it := range items {
		got = append(got, fmt.Sprintf("%s %d", it.Kind, it.ID))
	}
	if strings.Join(got, ", ") != "comment 1, review-comment 100" {
		t.Errorf("pending = %v, want comment 1 (alice's 👀 is not the member's) and review-comment 100", got)
	}
}
