package prs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
)

// fakePR is one pull request as the details fake serves it, over REST and
// over GraphQL alike.
type fakePR struct {
	pr        *githubv39.PullRequest
	labels    []string
	commits   []*githubv39.RepositoryCommit
	comments  []*githubv39.IssueComment
	reviews   []*githubv39.PullRequestReview
	inline    []*githubv39.PullRequestComment
	checkRuns []*githubv39.CheckRun
	statuses  []*githubv39.RepoStatus
}

// detailsFake is a GitHub serving a set of pull requests through both the REST
// calls the PR scanner makes and the GraphQL details query, from the same
// data, and recording what it is asked and what is written.
type detailsFake struct {
	mu      sync.Mutex
	prs     map[int]*fakePR
	gqlFail bool
	reads   []string
	writes  []string
	gqlRead int
}

const fakeBase = "/repos/test-owner/test-repo"

var prAliasRE = regexp.MustCompile(`pr(\d+): pullRequest`)

func (f *detailsFake) start(t *testing.T) *githubv39.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	gh := githubv39.NewClient(nil)
	gh.BaseURL, _ = url.Parse(srv.URL + "/")
	return gh
}

func (f *detailsFake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	body, _ := io.ReadAll(r.Body)

	if r.URL.Path == "/graphql" {
		f.graphQL(w, string(body))
		return
	}
	if r.Method != http.MethodGet {
		f.writes = append(f.writes, r.Method+" "+r.URL.Path+" "+strings.TrimSpace(string(body)))
	} else {
		f.reads = append(f.reads, r.URL.Path)
	}

	enc := json.NewEncoder(w)
	for num, p := range f.prs {
		sha := p.pr.GetHead().GetSHA()
		switch r.URL.Path {
		case fmt.Sprintf("%s/pulls/%d", fakeBase, num):
			_ = enc.Encode(p.pr)
			return
		case fmt.Sprintf("%s/pulls/%d/commits", fakeBase, num):
			_ = enc.Encode(p.commits)
			return
		case fmt.Sprintf("%s/issues/%d/comments", fakeBase, num):
			if r.Method == http.MethodGet {
				_ = enc.Encode(p.comments)
			} else {
				_ = enc.Encode(&githubv39.IssueComment{ID: githubv39.Int64(1)})
			}
			return
		case fmt.Sprintf("%s/pulls/%d/reviews", fakeBase, num):
			_ = enc.Encode(p.reviews)
			return
		case fmt.Sprintf("%s/pulls/%d/comments", fakeBase, num):
			_ = enc.Encode(p.inline)
			return
		case fmt.Sprintf("%s/commits/%s/check-runs", fakeBase, sha):
			_ = enc.Encode(map[string]interface{}{"total_count": len(p.checkRuns), "check_runs": p.checkRuns})
			return
		case fmt.Sprintf("%s/commits/%s/statuses", fakeBase, sha):
			_ = enc.Encode(p.statuses)
			return
		case fmt.Sprintf("%s/issues/%d/labels", fakeBase, num):
			_ = enc.Encode([]*githubv39.Label{})
			return
		}
	}
	if strings.HasSuffix(r.URL.Path, "/reactions") {
		if r.Method == http.MethodGet {
			_ = enc.Encode([]*githubv39.Reaction{})
		} else {
			_ = enc.Encode(&githubv39.Reaction{ID: githubv39.Int64(1)})
		}
		return
	}
	// Referenced issues, assignee writes and anything else.
	_ = enc.Encode(map[string]interface{}{"number": 1, "labels": []interface{}{}})
}

// graphQL answers the details query from the same pull requests the REST
// routes serve, a review's reactions with none, and a mutation as a write.
func (f *detailsFake) graphQL(w http.ResponseWriter, body string) {
	var req struct {
		Query string `json:"query"`
	}
	_ = json.Unmarshal([]byte(body), &req)
	enc := json.NewEncoder(w)
	switch {
	case strings.Contains(req.Query, "pullRequest(number:"):
		f.gqlRead++
		if f.gqlFail {
			_ = enc.Encode(map[string]interface{}{"errors": []map[string]string{{"message": "something went wrong"}}})
			return
		}
		repo := map[string]interface{}{}
		for _, m := range prAliasRE.FindAllStringSubmatch(req.Query, -1) {
			num, _ := strconv.Atoi(m[1])
			if p := f.prs[num]; p != nil {
				repo["pr"+m[1]] = p.gql()
			}
		}
		_ = enc.Encode(map[string]interface{}{"data": map[string]interface{}{"repository": repo}})
	case strings.HasPrefix(strings.TrimSpace(req.Query), "mutation"):
		f.writes = append(f.writes, "GRAPHQL "+strings.TrimSpace(body))
		_ = enc.Encode(map[string]interface{}{"data": map[string]interface{}{"addReaction": map[string]interface{}{"reaction": map[string]string{"content": "EYES"}}}})
	default:
		_ = enc.Encode(map[string]interface{}{"data": map[string]interface{}{"node": map[string]interface{}{"reactions": map[string]interface{}{"nodes": []interface{}{}}}}})
	}
}

// gqlActor is a REST user as GraphQL reports it: a Bot without the "[bot]"
// suffix REST adds.
func gqlActorOf(u *githubv39.User) map[string]interface{} {
	if u == nil {
		return nil
	}
	if u.GetType() == "Bot" {
		return map[string]interface{}{"__typename": "Bot", "login": strings.TrimSuffix(u.GetLogin(), "[bot]")}
	}
	return map[string]interface{}{"__typename": "User", "login": u.GetLogin()}
}

func gqlTime(t *time.Time) interface{} {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}

// pageOf is a GraphQL connection of the first n of nodes, with the whole
// count, as the query's page sizes read it.
func pageOf(nodes []interface{}, n int) map[string]interface{} {
	total := len(nodes)
	if len(nodes) > n {
		nodes = nodes[:n]
	}
	return map[string]interface{}{"totalCount": total, "nodes": nodes}
}

func (p *fakePR) gql() map[string]interface{} {
	pr := p.pr
	mergeable := "UNKNOWN"
	if pr.Mergeable != nil {
		mergeable = map[bool]string{true: "MERGEABLE", false: "CONFLICTING"}[*pr.Mergeable]
	}
	state := "OPEN"
	if pr.GetState() != "open" {
		state = "CLOSED"
	}
	var labels, commits, comments, reviews []interface{}
	for _, l := range p.labels {
		labels = append(labels, map[string]interface{}{"name": l})
	}
	for _, c := range p.commits {
		commits = append(commits, map[string]interface{}{"commit": map[string]interface{}{"oid": c.GetSHA(), "committedDate": gqlTime(c.GetCommit().GetCommitter().Date)}})
	}
	for _, c := range p.comments {
		comments = append(comments, map[string]interface{}{
			"id": c.GetNodeID(), "databaseId": c.GetID(), "author": gqlActorOf(c.User), "authorAssociation": c.GetAuthorAssociation(),
			"body": c.GetBody(), "createdAt": gqlTime(c.CreatedAt), "updatedAt": gqlTime(c.UpdatedAt), "url": c.GetHTMLURL(),
		})
	}
	for _, rv := range p.reviews {
		var inline []interface{}
		for _, rc := range p.inline {
			if rc.GetPullRequestReviewID() != rv.GetID() {
				continue
			}
			inline = append(inline, map[string]interface{}{
				"id": rc.GetNodeID(), "databaseId": rc.GetID(), "author": gqlActorOf(rc.User), "authorAssociation": rc.GetAuthorAssociation(),
				"body": rc.GetBody(), "createdAt": gqlTime(rc.CreatedAt), "updatedAt": gqlTime(rc.UpdatedAt), "path": rc.GetPath(), "url": rc.GetHTMLURL(),
				"commit": map[string]interface{}{"oid": rc.GetCommitID()},
			})
		}
		reviews = append(reviews, map[string]interface{}{
			"id": rv.GetNodeID(), "databaseId": rv.GetID(), "author": gqlActorOf(rv.User), "authorAssociation": rv.GetAuthorAssociation(),
			"state": rv.GetState(), "body": rv.GetBody(), "submittedAt": gqlTime(rv.SubmittedAt), "url": rv.GetHTMLURL(),
			"commit":   map[string]interface{}{"oid": rv.GetCommitID()},
			"comments": pageOf(inline, 50),
		})
	}
	return map[string]interface{}{
		"number": pr.GetNumber(), "headRefOid": pr.GetHead().GetSHA(), "headRefName": pr.GetHead().GetRef(), "baseRefName": pr.GetBase().GetRef(),
		"title": pr.GetTitle(), "body": pr.GetBody(), "state": state, "isDraft": pr.GetDraft(), "mergeable": mergeable,
		"createdAt": gqlTime(pr.CreatedAt), "updatedAt": gqlTime(pr.UpdatedAt), "url": pr.GetHTMLURL(),
		"author":          gqlActorOf(pr.User),
		"labels":          pageOf(labels, 100),
		"assignees":       pageOf(nil, 50),
		"mergeQueueEntry": nil,
		"commits":         pageOf(commits, 100),
		"comments":        pageOf(comments, 100),
		"reviews":         pageOf(reviews, 50),
	}
}

// detailsScenario is a set of pull requests that between them take every path
// an evaluation can: a review, a conflict, new feedback in a comment, in a
// review asking for changes and from an App, a failing check and a failing
// status, mergeability still being computed, and more comments than the
// GraphQL query reads.
func detailsScenario() map[int]*fakePR {
	now := time.Now().UTC().Truncate(time.Second)
	at := func(d time.Duration) *time.Time { t := now.Add(-d); return &t }
	yes, no := true, false
	user := func(login string) *githubv39.User {
		return &githubv39.User{Login: githubv39.String(login), Type: githubv39.String("User")}
	}
	newPR := func(num int, mergeable *bool, labels ...string) *fakePR {
		sha := fmt.Sprintf("sha%d", num)
		p := &fakePR{
			pr: &githubv39.PullRequest{
				Number: githubv39.Int(num), Title: githubv39.String(fmt.Sprintf("PR %d", num)), Body: githubv39.String("A change."),
				State: githubv39.String("open"), Draft: githubv39.Bool(false), Mergeable: mergeable,
				User:      user("bot1"),
				Head:      &githubv39.PullRequestBranch{SHA: githubv39.String(sha), Ref: githubv39.String(fmt.Sprintf("change-%d", num))},
				Base:      &githubv39.PullRequestBranch{Ref: githubv39.String("main")},
				CreatedAt: at(48 * time.Hour), UpdatedAt: at(30 * time.Minute),
				HTMLURL: githubv39.String(fmt.Sprintf("https://github.com/test-owner/test-repo/pull/%d", num)),
			},
			labels: append([]string{"overseer"}, labels...),
			commits: []*githubv39.RepositoryCommit{{
				SHA:    githubv39.String(sha),
				Commit: &githubv39.Commit{Committer: &githubv39.CommitAuthor{Date: at(2 * time.Hour)}},
			}},
			checkRuns: []*githubv39.CheckRun{{
				Name: githubv39.String("build"), Status: githubv39.String("completed"), Conclusion: githubv39.String("success"),
				StartedAt: &githubv39.Timestamp{Time: *at(100 * time.Minute)}, CompletedAt: &githubv39.Timestamp{Time: *at(90 * time.Minute)},
			}},
		}
		return p
	}
	comment := func(id int64, author *githubv39.User, assoc string, ago time.Duration, body string) *githubv39.IssueComment {
		return &githubv39.IssueComment{
			ID: githubv39.Int64(id), NodeID: githubv39.String(fmt.Sprintf("IC_%d", id)), User: author,
			AuthorAssociation: githubv39.String(assoc), Body: githubv39.String(body), CreatedAt: at(ago), UpdatedAt: at(ago),
			HTMLURL: githubv39.String(fmt.Sprintf("https://github.com/test-owner/test-repo/pull/1#issuecomment-%d", id)),
		}
	}

	prs := map[int]*fakePR{}

	// Green and labelled for review: a review.
	prs[10] = newPR(10, &yes, "overseer/review")

	// Conflicting: a rebase.
	prs[11] = newPR(11, &no)

	// A trusted reviewer's comment after the last commit: comments.
	prs[12] = newPR(12, &yes)
	prs[12].comments = []*githubv39.IssueComment{comment(1201, user("alice"), "MEMBER", time.Hour, "Please rename this.")}

	// A failing check: an investigation.
	prs[13] = newPR(13, &yes)
	prs[13].checkRuns[0].Conclusion = githubv39.String("failure")

	// Mergeability still being computed.
	prs[14] = newPR(14, nil)

	// A review asking for changes on the head, with an inline comment.
	prs[15] = newPR(15, &yes)
	prs[15].reviews = []*githubv39.PullRequestReview{{
		ID: githubv39.Int64(1501), NodeID: githubv39.String("PRR_1501"), User: user("alice"), AuthorAssociation: githubv39.String("MEMBER"),
		State: githubv39.String("CHANGES_REQUESTED"), Body: githubv39.String("Some changes."), SubmittedAt: at(time.Hour),
		CommitID: githubv39.String("sha15"), HTMLURL: githubv39.String("https://github.com/test-owner/test-repo/pull/15#pullrequestreview-1501"),
	}}
	prs[15].inline = []*githubv39.PullRequestComment{{
		ID: githubv39.Int64(1502), NodeID: githubv39.String("PRRC_1502"), PullRequestReviewID: githubv39.Int64(1501), User: user("alice"),
		AuthorAssociation: githubv39.String("MEMBER"), Body: githubv39.String("This leaks."), Path: githubv39.String("main.go"),
		CreatedAt: at(time.Hour), UpdatedAt: at(time.Hour), CommitID: githubv39.String("sha15"),
		HTMLURL: githubv39.String("https://github.com/test-owner/test-repo/pull/15#discussion_r1502"),
	}}

	// More comments than the GraphQL query reads (all before the last commit,
	// by the PR's own author): read the REST way, then labelled ready.
	prs[16] = newPR(16, &yes)
	for i := 0; i < 101; i++ {
		prs[16].comments = append(prs[16].comments, comment(int64(160000+i), user("bot1"), "NONE", 5*time.Hour, "Working on it."))
	}

	// An App's comment, allowlisted: GraphQL names it without "[bot]".
	prs[17] = newPR(17, &yes)
	prs[17].comments = []*githubv39.IssueComment{comment(1701, &githubv39.User{Login: githubv39.String("helper-app[bot]"), Type: githubv39.String("Bot")}, "NONE", time.Hour, "This test is flaky.")}

	// A failing commit status (statuses are read the REST way either way).
	prs[18] = newPR(18, &yes)
	prs[18].statuses = []*githubv39.RepoStatus{{
		Context: githubv39.String("tide"), State: githubv39.String("failure"),
		CreatedAt: at(80 * time.Minute), UpdatedAt: at(80 * time.Minute),
	}}
	return prs
}

// candidatesOf is the listing the scanner would have handed evaluateAll.
func candidatesOf(prs map[int]*fakePR) []*githubv39.Issue {
	var nums []int
	for n := range prs {
		nums = append(nums, n)
	}
	slices.Sort(nums)
	var out []*githubv39.Issue
	for _, n := range nums {
		p := prs[n]
		var labels []*githubv39.Label
		for _, l := range p.labels {
			labels = append(labels, &githubv39.Label{Name: githubv39.String(l)})
		}
		out = append(out, &githubv39.Issue{
			Number: githubv39.Int(n), UpdatedAt: p.pr.UpdatedAt, Labels: labels,
			Assignees:        []*githubv39.User{{Login: githubv39.String("bot1")}},
			PullRequestLinks: &githubv39.PullRequestLinks{},
		})
	}
	return out
}

type detailsOutcome struct {
	tasks   []string
	writes  []string
	reads   []string
	gqlRead int
}

// runDetailsScenario evaluates the scenario's pull requests once, with or
// without the GraphQL details, and returns what was queued and written.
func runDetailsScenario(t *testing.T, graphQL, gqlFail bool) detailsOutcome {
	t.Helper()
	f := &detailsFake{prs: detailsScenario(), gqlFail: gqlFail}
	dir := t.TempDir()
	s, _ := newTestScanner(t, dir, testOpts{
		GitHub:           f.start(t),
		BotUsers:         []string{"bot1"},
		GitHubLogin:      "watcher",
		TriggerLabel:     "overseer",
		ReviewerLogins:   []string{"reviewbot"},
		AllowlistedBots:  []string{"helper-app[bot]"},
		AllowlistedUsers: []string{"alice"},
	})
	s.cfg.GraphQLPRDetails = graphQL
	s.evaluateAll(context.Background(), candidatesOf(f.prs))

	entries, err := os.ReadDir(filepath.Join(dir, "incoming"))
	if err != nil {
		t.Fatal(err)
	}
	var out detailsOutcome
	for _, e := range entries {
		out.tasks = append(out.tasks, e.Name())
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out.writes = append([]string(nil), f.writes...)
	out.reads = append([]string(nil), f.reads...)
	out.gqlRead = f.gqlRead
	slices.Sort(out.tasks)
	slices.Sort(out.writes)
	return out
}

// detailCalls counts the REST reads the GraphQL details query replaces: the
// pull request, its commits, comments, reviews and inline comments.
func detailCalls(reads []string) int {
	n := 0
	detail := regexp.MustCompile(`/pulls/\d+$|/pulls/\d+/(commits|reviews|comments)$|/issues/\d+/comments$`)
	for _, r := range reads {
		if detail.MatchString(r) {
			n++
		}
	}
	return n
}

// With the GraphQL details on, the scanner queues the same tasks and writes
// the same things to GitHub as with the REST calls, for every path an
// evaluation can take; the REST detail reads are replaced by one query for
// the batch, but for the pull request with more comments than the query
// reads, which falls back to REST.
func TestGraphQLDetailsDecideAsREST(t *testing.T) {
	rest := runDetailsScenario(t, false, false)
	gql := runDetailsScenario(t, true, false)

	if !slices.Equal(rest.tasks, gql.tasks) {
		t.Errorf("tasks differ:\n REST    %v\n GraphQL %v", rest.tasks, gql.tasks)
	}
	if !slices.Equal(rest.writes, gql.writes) {
		t.Errorf("writes differ:\n REST    %q\n GraphQL %q", rest.writes, gql.writes)
	}
	// The scenario takes the paths it is meant to, and writes to GitHub (the
	// acknowledging reactions, the readiness labels), so equal writes mean
	// something.
	if len(rest.writes) == 0 {
		t.Error("the scenario wrote nothing to GitHub")
	}
	for _, want := range []string{"task-pr-10-rule-review.yaml", "task-pr-11-rule-conflict.yaml", "task-pr-12-rule-comments.yaml", "task-pr-13-rule-ci.yaml", "task-pr-15-rule-comments.yaml", "task-pr-17-rule-comments.yaml", "task-pr-18-rule-ci.yaml"} {
		if !slices.Contains(rest.tasks, want) {
			t.Errorf("the scenario did not queue %s: %v", want, rest.tasks)
		}
	}
	if gql.gqlRead != 1 {
		t.Errorf("GraphQL details queries = %d, want 1 for the batch", gql.gqlRead)
	}
	restDetail, gqlDetail := detailCalls(rest.reads), detailCalls(gql.reads)
	// PR 16 alone is read the REST way: its pull request, commits,
	// comments, reviews and inline comments.
	if gqlDetail != 5 {
		t.Errorf("REST detail reads with GraphQL on = %d, want 5 (PR 16's fallback)", gqlDetail)
	}
	t.Logf("9 PRs: REST detail reads %d (+%d total REST reads); GraphQL on: %d detail reads, %d GraphQL query, %d total REST reads",
		restDetail, len(rest.reads), gqlDetail, gql.gqlRead, len(gql.reads))
}

// A failed details query leaves every pull request to the REST calls, with
// the same outcome.
func TestGraphQLDetailsFallBackOnError(t *testing.T) {
	rest := runDetailsScenario(t, false, false)
	failed := runDetailsScenario(t, true, true)
	if !slices.Equal(rest.tasks, failed.tasks) || !slices.Equal(rest.writes, failed.writes) {
		t.Errorf("a failed GraphQL query changed the outcome:\n REST   %v %q\n failed %v %q", rest.tasks, rest.writes, failed.tasks, failed.writes)
	}
	if got, want := detailCalls(failed.reads), detailCalls(rest.reads); got != want {
		t.Errorf("REST detail reads after a failed query = %d, want all %d", got, want)
	}
}
