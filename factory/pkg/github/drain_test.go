package github

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	githubv39 "github.com/google/go-github/v39/github"
	"golang.org/x/oauth2"
)

// A repeat read through go-github is a free 304 - over HTTP/2, as GitHub
// is reached, and for a body of any size. go-github closes a body once it
// has decoded it, before the read that sees EOF, and the cache stores only
// a body read to EOF: every repeat was a charged 200.
func TestRepeatReadsThroughGoGithubAreFree(t *testing.T) {
	for _, h2 := range []bool{false, true} {
		for _, size := range []int{100, 60000} {
			t.Run(fmt.Sprintf("h2=%v,size=%d", h2, size), func(t *testing.T) {
				body := fmt.Sprintf(`{"type":"file","name":"SKILL.md","path":"SKILL.md","content":"%s","encoding":"base64"}`, strings.Repeat("A", size))
				srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("ETag", `"v1"`)
					w.Header().Set("Cache-Control", "private, max-age=60")
					if r.Header.Get("If-None-Match") == `"v1"` {
						w.WriteHeader(http.StatusNotModified)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(body))
				}))
				srv.EnableHTTP2 = h2
				srv.StartTLS()
				defer srv.Close()
				// The test server's client trusts its certificate, and speaks
				// HTTP/2 to it when it serves HTTP/2.
				ctx := context.WithValue(context.Background(), oauth2.HTTPClient, srv.Client())
				gh := githubv39.NewClient(cachedHTTPClient(ctx, "drain-test-"+t.Name()))
				gh.BaseURL, _ = gh.BaseURL.Parse(srv.URL + "/")

				before := ProcessUsage()
				for i := 0; i < 3; i++ {
					fc, _, _, err := gh.Repositories.GetContents(ctx, "o", "r", "SKILL.md", nil)
					if err != nil {
						t.Fatal(err)
					}
					if fc.GetName() != "SKILL.md" {
						t.Fatalf("read %d decoded %+v", i, fc)
					}
				}
				d := ProcessUsage().Minus(before)
				if d.Outcomes[OutcomeRead] != 1 || d.Outcomes[OutcomeFree] != 2 {
					t.Errorf("3 reads = %d charged, %d free; want 1 charged, 2 free 304s", d.Outcomes[OutcomeRead], d.Outcomes[OutcomeFree])
				}
			})
		}
	}
}
