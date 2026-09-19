package repoboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// Proves the conditional-request wiring end to end: the first GET caches
// the body + ETag, the second GET carries If-None-Match, the server's 304
// (which GitHub does not count against the rate limit) is transparently
// answered from cache — and a different token never sees another token's
// cached entry (Vary: Authorization).
func TestGithubClientConditionalRequests(t *testing.T) {
	var full, conditional atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Vary", "Accept, Authorization")
		w.Header().Set("Cache-Control", "private, max-age=0") // force revalidation
		w.Header().Set("ETag", `"abc123"`)
		if r.Header.Get("If-None-Match") == `"abc123"` {
			conditional.Add(1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		full.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"number": 7, "title": "cached"}]`))
	}))
	defer srv.Close()

	get := func(token string) string {
		gh := githubClientFromToken(context.Background(), token)
		req, _ := http.NewRequest("GET", srv.URL+"/repos/o/r/issues", nil)
		resp, err := gh.Client().Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()
		buf := make([]byte, 64)
		n, _ := resp.Body.Read(buf)
		return string(buf[:n])
	}

	if body := get("token-a"); body == "" {
		t.Fatal("first fetch returned no body")
	}
	if body := get("token-a"); body == "" {
		t.Fatal("revalidated fetch must serve the cached body on 304")
	}
	if full.Load() != 1 || conditional.Load() != 1 {
		t.Fatalf("expected 1 full + 1 conditional request, got %d full / %d conditional", full.Load(), conditional.Load())
	}

	// A different token must not ride the first token's cache entry.
	_ = get("token-b")
	if full.Load() != 2 {
		t.Fatalf("second token must issue a full request (Vary: Authorization), got %d full", full.Load())
	}
}
