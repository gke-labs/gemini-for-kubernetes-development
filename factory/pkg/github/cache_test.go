package github

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// etagServer answers every GET with an ETag, and with 304 when asked
// conditionally for it; it records what each request carried.
type etagServer struct {
	mu       sync.Mutex
	requests []http.Header
}

func (s *etagServer) handler(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.requests = append(s.requests, r.Header.Clone())
	s.mu.Unlock()
	w.Header().Set("ETag", `"v1"`)
	w.Header().Set("Cache-Control", "private, max-age=0")
	if r.Header.Get("If-None-Match") == `"v1"` {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = io.WriteString(w, `{"number": 1}`)
}

func get(t *testing.T, c *http.Client, url string) (int, string) {
	t.Helper()
	resp, err := c.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// A repeated GET with one token goes out conditional, and the 304 reaches
// the caller as the cached 200.
func TestCachedClientSendsConditionalRequests(t *testing.T) {
	s := &etagServer{}
	srv := httptest.NewServer(http.HandlerFunc(s.handler))
	defer srv.Close()
	c := cachedHTTPClient(context.Background(), "token-conditional")

	for i := 0; i < 2; i++ {
		if code, body := get(t, c, srv.URL+"/repos/o/r/pulls/1"); code != http.StatusOK || body != `{"number": 1}` {
			t.Fatalf("GET %d = %d %q, want the 200 and its body", i+1, code, body)
		}
	}
	if len(s.requests) != 2 {
		t.Fatalf("server saw %d requests, want 2", len(s.requests))
	}
	if got := s.requests[1].Get("If-None-Match"); got != `"v1"` {
		t.Errorf("second request If-None-Match = %q, want the ETag", got)
	}
	if got := s.requests[1].Get("Authorization"); got != "Bearer token-conditional" {
		t.Errorf("second request Authorization = %q, want the token", got)
	}
	// A new client for the same token shares the cache.
	get(t, cachedHTTPClient(context.Background(), "token-conditional"), srv.URL+"/repos/o/r/pulls/1")
	if got := s.requests[2].Get("If-None-Match"); got != `"v1"` {
		t.Errorf("another client of the token asked unconditionally (If-None-Match %q)", got)
	}
}

// A response GitHub says is fresh for a minute is still checked: a read
// after a write sees the write.
func TestCachedClientRevalidatesFreshResponses(t *testing.T) {
	var conditional int
	version := `"v1"`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", version)
		w.Header().Set("Cache-Control", "private, max-age=60")
		w.Header().Set("Date", time.Now().UTC().Format(http.TimeFormat))
		if r.Header.Get("If-None-Match") != "" {
			conditional++
			if r.Header.Get("If-None-Match") == version {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
		_, _ = io.WriteString(w, version)
	}))
	defer srv.Close()
	c := cachedHTTPClient(context.Background(), "token-fresh")
	get(t, c, srv.URL+"/repos/o/r/issues/1/comments")
	version = `"v2"` // a comment was posted
	if _, body := get(t, c, srv.URL+"/repos/o/r/issues/1/comments"); body != `"v2"` {
		t.Errorf("read after the write = %q, want the new state", body)
	}
	if conditional != 1 {
		t.Errorf("%d conditional requests, want the second GET checked with GitHub", conditional)
	}
}

// Tokens never share entries: another token's first GET is unconditional.
func TestCachedClientKeepsTokensApart(t *testing.T) {
	s := &etagServer{}
	srv := httptest.NewServer(http.HandlerFunc(s.handler))
	defer srv.Close()
	get(t, cachedHTTPClient(context.Background(), "token-alice"), srv.URL+"/repos/o/r/issues")
	get(t, cachedHTTPClient(context.Background(), "token-bob"), srv.URL+"/repos/o/r/issues")
	if got := s.requests[1].Get("If-None-Match"); got != "" {
		t.Errorf("bob's first request reused alice's entry (If-None-Match %q)", got)
	}
	if cacheFor("token-alice") == cacheFor("token-bob") {
		t.Error("two tokens share one cache")
	}
}

// A POST (GraphQL, a write) is not cached.
func TestCachedClientPassesWritesThrough(t *testing.T) {
	s := &etagServer{}
	srv := httptest.NewServer(http.HandlerFunc(s.handler))
	defer srv.Close()
	c := cachedHTTPClient(context.Background(), "token-post")
	for i := 0; i < 2; i++ {
		resp, err := c.Post(srv.URL+"/graphql", "application/json", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	if got := s.requests[1].Get("If-None-Match"); got != "" {
		t.Errorf("a POST went out conditional (If-None-Match %q)", got)
	}
}

// The cache keeps at most its bound, dropping the least recently used.
func TestLRUCacheBound(t *testing.T) {
	c := newLRUCache(2)
	c.Set("a", []byte("1"))
	c.Set("b", []byte("2"))
	c.Get("a") // a is now the most recently used
	c.Set("c", []byte("3"))
	if _, ok := c.Get("b"); ok {
		t.Error("b, the least recently used, was kept")
	}
	if _, ok := c.Get("a"); !ok {
		t.Error("a was dropped though used last")
	}
	if c.Len() != 2 {
		t.Errorf("len = %d, want 2", c.Len())
	}
	c.Delete("a")
	if _, ok := c.Get("a"); ok || c.Len() != 1 {
		t.Errorf("after Delete: len %d", c.Len())
	}
}
