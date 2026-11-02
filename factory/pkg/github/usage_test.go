package github

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// fakeGitHub answers like GitHub: an ETag on reads and 304 to a
// revalidation, the token's rate limit on every response.
func fakeGitHub(t *testing.T) *httptest.Server {
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
	t.Cleanup(srv.Close)
	return srv
}

func do(ctx context.Context, t *testing.T, c *http.Client, method, url string) {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, method, url, strings.NewReader(""))
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body) // the cache stores a response once its body is read
	resp.Body.Close()
}

func find(us []Usage, name string) *Usage {
	for i := range us {
		if us[i].Name == name {
			return &us[i]
		}
	}
	return nil
}

// Requests are counted under the cache, as GitHub bills them: a first read
// (200) is charged, its revalidation (304) is free, a write is charged, a
// GraphQL query is its own budget; by the component that asked and the
// token they went as, whose rate limit GitHub reports is kept.
func TestUsageCountsWhatGitHubCharges(t *testing.T) {
	srv := fakeGitHub(t)
	before := ProcessUsage()
	token := "usage-test-token-" + t.Name()
	NameToken(token, "watcher-test")
	c := cachedHTTPClient(context.Background(), token)
	graphs := WithComponent(context.Background(), "graphs-test")
	issues := WithComponent(context.Background(), "issues-test")
	do(graphs, t, c, http.MethodGet, srv.URL+"/repos/o/r/issues/7") // 200: charged
	do(graphs, t, c, http.MethodGet, srv.URL+"/repos/o/r/issues/7") // 304: free
	do(issues, t, c, http.MethodGet, srv.URL+"/repos/o/r/issues/8") // 200: charged (another issue, same endpoint)
	do(issues, t, c, http.MethodPost, srv.URL+"/repos/o/r/issues/7/labels")
	do(context.Background(), t, c, http.MethodPost, srv.URL+"/graphql")

	d := ProcessUsage().Minus(before)
	if d.Charged != 3 || d.Outcomes[OutcomeRead] != 2 || d.Outcomes[OutcomeFree] != 1 || d.Outcomes[OutcomeWrite] != 1 || d.Outcomes[OutcomeGraphQL] != 1 {
		t.Errorf("counts = charged %d, %v", d.Charged, d.Outcomes)
	}
	if e := find(d.Endpoints, "GET /repos/{r}/issues/{n}"); e == nil || e.Total != 3 || e.Charged != 2 || e.Statuses[304] != 1 {
		t.Errorf("GET /repos/{r}/issues/{n} = %+v", e)
	}
	if d.Endpoints[0].Name != "GET /repos/{r}/issues/{n}" {
		t.Errorf("most charged first: %v", d.Endpoints[0].Name)
	}
	if g := find(d.Components, "graphs-test"); g == nil || g.Charged != 1 || g.Outcomes[OutcomeFree] != 1 {
		t.Errorf("graphs-test = %+v", g)
	}
	if i := find(d.Components, "issues-test"); i == nil || i.Charged != 2 {
		t.Errorf("issues-test = %+v", i)
	}
	if o := find(d.Components, componentOther); o == nil || o.Outcomes[OutcomeGraphQL] != 1 {
		t.Errorf("untagged = %+v", o)
	}
	if tk := find(d.Tokens, "watcher-test"); tk == nil || tk.Charged != 3 {
		t.Errorf("token = %+v (tokens %+v)", tk, d.Tokens)
	}
	if rl := d.RateLimits["watcher-test"]["core"]; rl.Remaining != 4321 || rl.Limit != 5000 || rl.Used != 679 || rl.Reset.IsZero() {
		t.Errorf("core rate limit = %+v", rl)
	}
	sum := d.Summary(5)
	for _, want := range []string{
		"charged 3 (reads 2, writes 1), free 304s 1, graphql 1",
		"tokens: watcher-test 3 (4321 of 5000 left",
		"components: issues-test 2, graphs-test 1 (+1 free), other 1",
		"top: GET /repos/{r}/issues/{n} 2 (+1 free)",
	} {
		if !strings.Contains(sum, want) {
			t.Errorf("summary %q lacks %q", sum, want)
		}
	}
}

// A child process reports what it counted to the file its watch names, as
// it goes and at its end; the watch reads each report once into its own
// counts, under the child's component and token, and a line not yet
// complete waits.
func TestChildUsageReachesTheWatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.jsonl")
	t.Setenv(EnvUsageFile, path)
	stop := ReportUsage(time.Hour)

	child := WithComponent(context.Background(), "task:fix-test")
	processUsage.record(TokenID("bot-token"), componentOf(child), "POST /repos/{r}/pulls", OutcomeWrite, 201, nil, time.Now())
	stop()
	stop() // once

	// The watch: another process; here, the same counts read back in.
	before := ProcessUsage()
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString(`{"at":"2026-11-02T07:00:00Z","series":[{"token":"x"`) // a report half written
	f.Close()
	if err := IngestUsage(path); err != nil {
		t.Fatal(err)
	}
	if err := IngestUsage(path); err != nil { // nothing new: counted once
		t.Fatal(err)
	}
	d := ProcessUsage().Minus(before)
	if c := find(d.Components, "task:fix-test"); c == nil || c.Charged != 1 || c.Outcomes[OutcomeWrite] != 1 {
		t.Errorf("task:fix-test = %+v (components %+v)", c, d.Components)
	}
	if tk := find(d.Tokens, TokenID("bot-token")); tk == nil || tk.Charged != 1 {
		t.Errorf("bot token = %+v", tk)
	}
	if err := IngestUsage(filepath.Join(t.TempDir(), "none")); err != nil {
		t.Errorf("no file yet: %v", err)
	}
}

func TestTokenIDHidesTheToken(t *testing.T) {
	id := TokenID("ghp_secret")
	if strings.Contains(id, "secret") || !strings.HasPrefix(id, "token-") || len(id) != len("token-")+8 || id != TokenID("ghp_secret") {
		t.Errorf("TokenID = %q", id)
	}
}
