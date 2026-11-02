package github

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// An endpoint is the method and the path with its repository, numbers and
// SHAs templated, and its query's keys: one list read page by page, or
// one read of any issue, is one endpoint.
func TestEndpoint(t *testing.T) {
	for _, tc := range []struct{ method, path, query, want string }{
		{"GET", "/repos/o/r/issues/14659/timeline", "per_page=100&page=2", "GET /repos/{r}/issues/{n}/timeline?per_page"},
		{"GET", "/repos/o/r/issues", "creator=watcher&state=all&sort=created&direction=desc&per_page=100", "GET /repos/{r}/issues?creator,direction,per_page,sort,state"},
		{"GET", "/repos/o/r/commits/4493127fabc123/check-runs", "filter=latest", "GET /repos/{r}/commits/{sha}/check-runs?filter"},
		{"POST", "/repos/o/r/issues/12/labels", "", "POST /repos/{r}/issues/{n}/labels"},
		{"GET", "/repos/o/r/contents/.agents/graphs/kcc-greenfield.yaml", "", "GET /repos/{r}/contents/.agents/graphs/kcc-greenfield.yaml"},
		{"POST", "/graphql", "", "POST /graphql"},
		{"GET", "/rate_limit", "", "GET /rate_limit"},
	} {
		if got := Endpoint(tc.method, tc.path, tc.query); got != tc.want {
			t.Errorf("Endpoint(%s %s?%s) = %q, want %q", tc.method, tc.path, tc.query, got, tc.want)
		}
	}
}

// Requests are counted under the cache, as GitHub bills them: a first read
// (200) is charged, its revalidation (304) is free, a write is charged, a
// GraphQL query is its own budget; and the rate limit GitHub reports is
// kept.
func TestUsageCountsWhatGitHubCharges(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Resource", "core")
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "4321")
		w.Header().Set("X-RateLimit-Used", "679")
		w.Header().Set("X-RateLimit-Reset", "1793604064")
		switch {
		case r.Method == http.MethodGet && r.Header.Get("If-None-Match") == `"v1"`:
			w.WriteHeader(http.StatusNotModified)
		case r.Method == http.MethodGet:
			w.Header().Set("ETag", `"v1"`)
			w.Header().Set("Cache-Control", "private, max-age=60")
			_, _ = w.Write([]byte(`{"number":7}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	before := Usage()
	c := cachedHTTPClient(context.Background(), "usage-test-token-"+t.Name())
	do := func(method, path string) {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(""))
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body) // the cache stores a response once its body is read
		resp.Body.Close()
	}
	do(http.MethodGet, "/repos/o/r/issues/7") // 200: charged
	do(http.MethodGet, "/repos/o/r/issues/7") // 304: free
	do(http.MethodGet, "/repos/o/r/issues/8") // 200: charged (another issue, same endpoint)
	do(http.MethodPost, "/repos/o/r/issues/7/labels")
	do(http.MethodPost, "/graphql")

	d := Usage().Minus(before)
	if d.Charged != 3 || d.Outcomes[OutcomeRead] != 2 || d.Outcomes[OutcomeFree] != 1 || d.Outcomes[OutcomeWrite] != 1 || d.Outcomes[OutcomeGraphQL] != 1 {
		t.Errorf("counts = charged %d, %v", d.Charged, d.Outcomes)
	}
	var issue *EndpointUsage
	for i := range d.Endpoints {
		if d.Endpoints[i].Endpoint == "GET /repos/{r}/issues/{n}" {
			issue = &d.Endpoints[i]
		}
	}
	if issue == nil || issue.Total != 3 || issue.Charged != 2 || issue.Statuses[304] != 1 {
		t.Errorf("GET /repos/{r}/issues/{n} = %+v", issue)
	}
	if d.Endpoints[0].Endpoint != "GET /repos/{r}/issues/{n}" {
		t.Errorf("most charged first: %v", d.Endpoints[0].Endpoint)
	}
	if rl := d.RateLimit["core"]; rl.Remaining != 4321 || rl.Limit != 5000 || rl.Used != 679 || rl.Reset.IsZero() {
		t.Errorf("core rate limit = %+v", rl)
	}
	sum := d.Summary(5)
	for _, want := range []string{"charged 3 (reads 2, writes 1), free 304s 1, graphql 1", "core limit: 4321 of 5000 left", "top: GET /repos/{r}/issues/{n} 2 (+1 free)"} {
		if !strings.Contains(sum, want) {
			t.Errorf("summary %q lacks %q", sum, want)
		}
	}
}
