package repoboard

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/go-github/v39/github"
	"github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/clients"
)

// The deprecated auto settings become rules, once, and are set off.
func TestLegacyAutoSettingsConvertToRules(t *testing.T) {
	for _, tc := range []struct {
		auto boardv1alpha1.AutoSpec
		want []boardv1alpha1.AutoRule
	}{
		{boardv1alpha1.AutoSpec{Triage: "unclaimed"}, []boardv1alpha1.AutoRule{{Recipe: "triage", On: "issue", When: boardv1alpha1.AutoWhen{Unclaimed: true}}}},
		{boardv1alpha1.AutoSpec{Triage: "all"}, []boardv1alpha1.AutoRule{{Recipe: "triage", On: "issue"}}},
		{boardv1alpha1.AutoSpec{Fix: "assigned"}, []boardv1alpha1.AutoRule{{Recipe: "fix", On: "issue", When: boardv1alpha1.AutoWhen{AssignedToOwner: true}}}},
		{boardv1alpha1.AutoSpec{Review: "requested"}, []boardv1alpha1.AutoRule{{Recipe: "review", On: "pr", When: boardv1alpha1.AutoWhen{ReviewRequestedFromOwner: true}}}},
		{boardv1alpha1.AutoSpec{Review: "all"}, []boardv1alpha1.AutoRule{{Recipe: "review", On: "pr"}}},
	} {
		g := gomega.NewWithT(t)
		auto := tc.auto
		g.Expect(auto.ConvertLegacy()).To(gomega.BeTrue())
		g.Expect(auto.Rules).To(gomega.Equal(tc.want))
		g.Expect([]string{auto.Triage, auto.Fix, auto.Review}).To(gomega.Equal([]string{"off", "off", "off"}))
		g.Expect(auto.ConvertLegacy()).To(gomega.BeFalse(), "converted twice")
	}
	off := boardv1alpha1.AutoSpec{Triage: "off", Fix: "off", Review: "off"}
	if off.ConvertLegacy() {
		t.Error("settings all off converted into something")
	}
}

// The controller converts a board's deprecated settings on the board
// itself.
func TestReconcileConvertsTheBoardsAutoSettings(t *testing.T) {
	g := gomega.NewWithT(t)
	r := newTestReconciler(newFakeLauncher(), testGithubClient(`[]`), testBoard(nil), githubSecret())
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	board := &boardv1alpha1.RepoBoard{}
	g.Expect(r.Get(context.Background(), client.ObjectKey{Namespace: "alice", Name: "test-board"}, board)).To(gomega.Succeed())
	g.Expect(board.Spec.Auto.Rules).To(gomega.Equal([]boardv1alpha1.AutoRule{{Recipe: "fix", On: "issue", When: boardv1alpha1.AutoWhen{AssignedToOwner: true}}}))
	g.Expect(board.Spec.Auto.Fix).To(gomega.Equal("off"))
}

// A rule the catalog cannot run is skipped and said why.
func TestAutoRulesAreValidatedAgainstTheCatalog(t *testing.T) {
	rules := []boardv1alpha1.AutoRule{
		{Recipe: "summarize", On: "issue"},
		{Recipe: "nope", On: "issue"},
		{Recipe: "care", On: "pr"}, // care starts on my-pr only
		{Recipe: "fix", On: "issue", When: boardv1alpha1.AutoWhen{ReviewRequestedFromOwner: true}},
	}
	catalog, _, _ := (&fakeLauncher{}).Recipes(context.Background(), "", "")
	valid, errs := validRules(rules, catalog)
	if len(valid) != 1 || valid[0].Recipe != "summarize" {
		t.Errorf("valid = %+v, want summarize alone", valid)
	}
	if len(errs) != 3 || !strings.Contains(errs[0], `no recipe "nope"`) || !strings.Contains(errs[1], "does not run on every pr") || !strings.Contains(errs[2], "no review requests") {
		t.Errorf("errors = %q", errs)
	}
}

// Each condition narrows the items; none set is every item.
func TestAutoRuleConditions(t *testing.T) {
	user := func(login string) *github.User { return &github.User{Login: github.String(login)} }
	label := func(name string) *github.Label { return &github.Label{Name: github.String(name)} }
	items := []autoItem{
		{number: 1},
		{number: 2, assignees: []*github.User{user("alice")}},
		{number: 3, assignees: []*github.User{user("bob")}, labels: []*github.Label{label("docs")}},
		{number: 4, reviewers: []*github.User{user("Alice")}},
	}
	for _, tc := range []struct {
		when boardv1alpha1.AutoWhen
		want []int
	}{
		{boardv1alpha1.AutoWhen{}, []int{1, 2, 3, 4}},
		{boardv1alpha1.AutoWhen{Unclaimed: true}, []int{1, 4}},
		{boardv1alpha1.AutoWhen{AssignedToOwner: true}, []int{2}},
		{boardv1alpha1.AutoWhen{ReviewRequestedFromOwner: true}, []int{4}},
		{boardv1alpha1.AutoWhen{Labels: []string{"docs"}}, []int{3}},
		{boardv1alpha1.AutoWhen{Unclaimed: true, Labels: []string{"docs"}}, nil},
	} {
		rule := autoRuleMatch{AutoRule: boardv1alpha1.AutoRule{When: tc.when}, owner: "alice"}
		var got []int
		for _, item := range items {
			if rule.matches(item) {
				got = append(got, item.number)
			}
		}
		if !slicesEqual(got, tc.want) {
			t.Errorf("when %+v matched %v, want %v", tc.when, got, tc.want)
		}
	}
}

func slicesEqual(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Any recipe can be automated: a rule for summarize on labelled issues
// files its Requests, up to the rule's daily cap, which counts the day's
// filings recorded on the board's status.
func TestAutoRuleForAnyRecipeWithADailyCap(t *testing.T) {
	g := gomega.NewWithT(t)
	ghClient := clients.NewGitHubClientFromHTTP(&http.Client{Transport: &mockRoundTripper{responses: map[string]func() *http.Response{
		"https://api.github.com/user": jsonResp(`{"login": "alice"}`),
		"https://api.github.com/repos/test/repo/issues?per_page=100&state=open": jsonResp(`[
			{"number": 50, "title": "a", "html_url": "https://github.com/test/repo/issues/50", "labels": [{"name": "needs-summary"}]},
			{"number": 51, "title": "b", "html_url": "https://github.com/test/repo/issues/51", "labels": [{"name": "needs-summary"}]},
			{"number": 52, "title": "c", "html_url": "https://github.com/test/repo/issues/52", "labels": []}
		]`),
	}}})
	board := testBoard(nil)
	board.Spec.Auto = boardv1alpha1.AutoSpec{Rules: []boardv1alpha1.AutoRule{
		{Recipe: "summarize", On: "issue", When: boardv1alpha1.AutoWhen{Labels: []string{"needs-summary"}}, MaxPerDay: 1},
	}}
	// One summarize filed an hour ago, one yesterday (no longer counted).
	board.Status.AutoFiled = []boardv1alpha1.AutoFiling{
		{Recipe: "summarize", At: metav1.NewTime(time.Now().Add(-25 * time.Hour))},
	}
	r := newTestReconciler(newFakeLauncher(), ghClient, board, githubSecret())
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	filed := subjects(recipeRequests(t, r))
	g.Expect(filed).To(gomega.HaveLen(1), "the cap of one a day")
	g.Expect(filed[0]).To(gomega.HavePrefix("alice:recipe/issue-5"))
	g.Expect(filed[0]).To(gomega.HaveSuffix("/summarize"))

	got := &boardv1alpha1.RepoBoard{}
	g.Expect(r.Get(context.Background(), client.ObjectKey{Namespace: "alice", Name: "test-board"}, got)).To(gomega.Succeed())
	g.Expect(got.Status.AutoFiled).To(gomega.HaveLen(1), "yesterday's filing dropped, today's recorded")
	g.Expect(got.Status.AutoRuleErrors).To(gomega.BeEmpty())
}

// Discovery lists only what the board's recency can make eligible: open
// issues updated since the cutoff (GitHub's since), and open PRs newest
// updated first, stopping at the first one older than it.
func TestAutoListingStopsAtTheRecencyCutoff(t *testing.T) {
	fresh := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	stale := time.Now().Add(-30 * 24 * time.Hour).UTC().Format(time.RFC3339)
	var issueQuery url.Values
	pages := 0
	ghClient := clients.NewGitHubClientFromHTTP(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := "[]"
		switch {
		case strings.HasSuffix(req.URL.Path, "/issues"):
			issueQuery = req.URL.Query()
		case strings.HasSuffix(req.URL.Path, "/pulls"):
			pages++
			body = fmt.Sprintf(`[{"number": 1, "updated_at": %q}, {"number": 2, "updated_at": %q}]`, fresh, stale)
		}
		header := http.Header{"Content-Type": []string{"application/json"}}
		if strings.HasSuffix(req.URL.Path, "/pulls") {
			// Claim a next page: the listing must stop at the stale PR anyway.
			header.Set("Link", `<https://api.github.com/repos/test/repo/pulls?page=2>; rel="next"`)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: header, Request: req}, nil
	})})
	r := newTestReconciler(newFakeLauncher(), ghClient, testBoard(nil), githubSecret())
	work := &workState{board: testBoard(nil), owner: "test", repo: "repo"}
	prs, err := r.listAutoItems(context.Background(), ghClient, work, "pr")
	if err != nil {
		t.Fatal(err)
	}
	if len(prs) != 1 || prs[0].number != 1 || pages != 1 {
		t.Errorf("PRs = %+v over %d pages, want #1 alone from one page", prs, pages)
	}
	if _, err := r.listAutoItems(context.Background(), ghClient, work, "issue"); err != nil {
		t.Fatal(err)
	}
	if issueQuery.Get("since") == "" {
		t.Errorf("issues listed without since: %v", issueQuery)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
