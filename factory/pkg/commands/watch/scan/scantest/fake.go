// Package scantest is a fake of GitHub's issue listing for tests: it filters,
// sorts and pages /repos/{owner}/{repo}/issues as GitHub does for the queries
// the watch makes (labels, assignee, creator, state, since, sort, direction,
// per_page, page), answers the GraphQL listing of open pull requests by an OR
// of labels (github.Client.OpenPRsLabelled), and counts the requests made to
// it.
package scantest

import (
	"cmp"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
)

// Item is one issue or pull request of the fake repository.
type Item struct {
	Number    int
	PR        bool
	State     string // "open" (default) or "closed"
	Labels    []string
	Assignees []string
	Author    string
	Title     string
	Body      string
	Created   time.Time
	Updated   time.Time
}

// Repo is the fake repository, served by Server.
type Repo struct {
	Owner, Name string

	mu    sync.Mutex
	items map[int]*Item
	// listCalls counts the requests to the issue listing.
	listCalls int
	// queries records each listing request's query string.
	queries []string
	// graphQLCalls counts the GraphQL label listings; graphQLLabels records
	// the labels each asked for.
	graphQLCalls  int
	graphQLLabels [][]string
	// Extra, when set, serves every request that is not the issue listing.
	Extra http.HandlerFunc
}

// NewRepo is an empty fake repository.
func NewRepo(owner, name string) *Repo {
	return &Repo{Owner: owner, Name: name, items: map[int]*Item{}}
}

// Put adds or replaces an item.
func (r *Repo) Put(it Item) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if it.State == "" {
		it.State = "open"
	}
	c := it
	r.items[it.Number] = &c
}

// Update changes an item in place and stamps it updated at t.
func (r *Repo) Update(num int, t time.Time, change func(*Item)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	it := r.items[num]
	change(it)
	it.Updated = t
}

// ListCalls is the number of issue listing requests served.
func (r *Repo) ListCalls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.listCalls
}

// Queries are the listing requests' query strings, in order.
func (r *Repo) Queries() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.queries)
}

// ResetCounts zeroes the request counters.
func (r *Repo) ResetCounts() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.listCalls = 0
	r.queries = nil
	r.graphQLCalls, r.graphQLLabels = 0, nil
}

// Server serves the fake repository; its URL is the base of a go-github client.
func (r *Repo) Server() *httptest.Server {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		path := fmt.Sprintf("/repos/%s/%s/issues", r.Owner, r.Name)
		if req.Method == http.MethodGet && req.URL.Path == path {
			r.list(w, req, srv.URL+path)
			return
		}
		if req.Method == http.MethodPost && req.URL.Path == "/graphql" {
			r.labelled(w, req)
			return
		}
		if r.Extra != nil {
			r.Extra(w, req)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{}"))
	}))
	return srv
}

func (r *Repo) list(w http.ResponseWriter, req *http.Request, base string) {
	q := req.URL.Query()
	r.mu.Lock()
	r.listCalls++
	r.queries = append(r.queries, req.URL.RawQuery)
	var out []*Item
	state := cmp.Or(q.Get("state"), "open")
	var since time.Time
	if s := q.Get("since"); s != "" {
		since, _ = time.Parse(time.RFC3339, s)
	}
	for _, it := range r.items {
		if state != "all" && it.State != state {
			continue
		}
		if l := q.Get("labels"); l != "" && !hasFold(it.Labels, l) {
			continue
		}
		if a := q.Get("assignee"); a != "" && !hasFold(it.Assignees, a) {
			continue
		}
		if c := q.Get("creator"); c != "" && !strings.EqualFold(it.Author, c) {
			continue
		}
		if !since.IsZero() && it.Updated.Before(since) {
			continue
		}
		c := *it
		out = append(out, &c)
	}
	r.mu.Unlock()

	sortBy := cmp.Or(q.Get("sort"), "created")
	desc := cmp.Or(q.Get("direction"), "desc") == "desc"
	slices.SortFunc(out, func(a, b *Item) int {
		ta, tb := a.Created, b.Created
		if sortBy == "updated" {
			ta, tb = a.Updated, b.Updated
		}
		c := ta.Compare(tb)
		if c == 0 {
			c = cmp.Compare(a.Number, b.Number)
		}
		if desc {
			return -c
		}
		return c
	})

	perPage, _ := strconv.Atoi(q.Get("per_page"))
	if perPage <= 0 {
		perPage = 30
	}
	page, _ := strconv.Atoi(q.Get("page"))
	if page <= 0 {
		page = 1
	}
	start := (page - 1) * perPage
	end := min(start+perPage, len(out))
	if start > len(out) {
		start = len(out)
	}
	if end < len(out) {
		next := url.Values{}
		for k, v := range q {
			next[k] = v
		}
		next.Set("page", strconv.Itoa(page+1))
		w.Header().Set("Link", fmt.Sprintf("<%s?%s>; rel=\"next\"", base, next.Encode()))
	}

	issues := make([]*githubv39.Issue, 0, end-start)
	for _, it := range out[start:end] {
		issues = append(issues, it.issue(r.Owner, r.Name))
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(issues)
}

// GraphQLCalls is how many GraphQL label listings were made, and the labels
// each asked for.
func (r *Repo) GraphQLCalls() (int, [][]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.graphQLCalls, slices.Clone(r.graphQLLabels)
}

// labelled answers the open pull requests carrying any of the query's
// labels, in one page.
func (r *Repo) labelled(w http.ResponseWriter, req *http.Request) {
	var body struct {
		Variables struct {
			Labels []string `json:"labels"`
		} `json:"variables"`
	}
	_ = json.NewDecoder(req.Body).Decode(&body)
	r.mu.Lock()
	r.graphQLCalls++
	r.graphQLLabels = append(r.graphQLLabels, body.Variables.Labels)
	type name struct {
		Name  string `json:"name,omitempty"`
		Login string `json:"login,omitempty"`
	}
	var nodes []map[string]any
	for _, it := range r.items {
		if !it.PR || it.State != "open" || !slices.ContainsFunc(body.Variables.Labels, func(l string) bool { return hasFold(it.Labels, l) }) {
			continue
		}
		var labels, assignees []name
		for _, l := range it.Labels {
			labels = append(labels, name{Name: l})
		}
		for _, a := range it.Assignees {
			assignees = append(assignees, name{Login: a})
		}
		nodes = append(nodes, map[string]any{
			"number": it.Number, "title": it.Title, "createdAt": it.Created, "updatedAt": it.Updated,
			"url":    fmt.Sprintf("https://github.com/%s/%s/pull/%d", r.Owner, r.Name, it.Number),
			"author": map[string]string{"__typename": "User", "login": it.Author},
			"labels": map[string]any{"nodes": labels}, "assignees": map[string]any{"nodes": assignees},
		})
	}
	r.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequests": map[string]any{
		"pageInfo": map[string]any{"hasNextPage": false, "endCursor": ""}, "nodes": nodes,
	}}}})
}

func (it *Item) issue(owner, repo string) *githubv39.Issue {
	created, updated := it.Created, it.Updated
	iss := &githubv39.Issue{
		Number:    githubv39.Int(it.Number),
		State:     githubv39.String(it.State),
		Title:     githubv39.String(it.Title),
		Body:      githubv39.String(it.Body),
		HTMLURL:   githubv39.String(fmt.Sprintf("https://github.com/%s/%s/issues/%d", owner, repo, it.Number)),
		CreatedAt: &created,
		UpdatedAt: &updated,
		User:      &githubv39.User{Login: githubv39.String(it.Author)},
	}
	if it.PR {
		iss.PullRequestLinks = &githubv39.PullRequestLinks{URL: githubv39.String(fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls/%d", owner, repo, it.Number))}
	}
	for _, l := range it.Labels {
		iss.Labels = append(iss.Labels, &githubv39.Label{Name: githubv39.String(l)})
	}
	for _, a := range it.Assignees {
		iss.Assignees = append(iss.Assignees, &githubv39.User{Login: githubv39.String(a)})
	}
	return iss
}

func hasFold(list []string, s string) bool {
	return slices.ContainsFunc(list, func(x string) bool { return strings.EqualFold(x, s) })
}
