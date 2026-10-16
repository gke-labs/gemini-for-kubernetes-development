package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gregjones/httpcache"
)

// forgetCaches drops the process's caches, as a new process starts
// without them.
func forgetCaches(t *testing.T) {
	t.Helper()
	tokenCachesMu.Lock()
	tokenCaches = map[string]httpcache.Cache{}
	tokenCachesMu.Unlock()
}

// With EnvCacheDir set, a new process starts from what the last left on
// disk: its first GET goes out conditional, and the 304 reaches it as the
// 200.
func TestDiskCacheOutlivesTheProcess(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvCacheDir, dir)
	forgetCaches(t)
	t.Cleanup(func() { forgetCaches(t) })
	s := &etagServer{}
	srv := httptest.NewServer(http.HandlerFunc(s.handler))
	defer srv.Close()

	if code, _ := get(t, cachedHTTPClient(context.Background(), "token-disk"), srv.URL+"/x"); code != http.StatusOK {
		t.Fatalf("first process: %d", code)
	}
	forgetCaches(t) // a new process
	code, body := get(t, cachedHTTPClient(context.Background(), "token-disk"), srv.URL+"/x")
	if code != http.StatusOK || body != `{"number": 1}` {
		t.Fatalf("second process: %d %q, want the cached 200", code, body)
	}
	if len(s.requests) != 2 || s.requests[1].Get("If-None-Match") != `"v1"` {
		t.Fatalf("second process's request was not conditional: %v", s.requests)
	}
}

// Each token has a directory of its own, named by its hash, never the
// token; it and its files are private.
func TestDiskCacheIsPerTokenAndPrivate(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvCacheDir, dir)
	forgetCaches(t)
	t.Cleanup(func() { forgetCaches(t) })
	s := &etagServer{}
	srv := httptest.NewServer(http.HandlerFunc(s.handler))
	defer srv.Close()

	get(t, cachedHTTPClient(context.Background(), "token-alice-secret"), srv.URL+"/x")
	get(t, cachedHTTPClient(context.Background(), "token-bob-secret"), srv.URL+"/x")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("%d token directories, want 2", len(entries))
	}
	for _, e := range entries {
		if e.Name() == "token-alice-secret" || e.Name() == "token-bob-secret" {
			t.Fatalf("a directory is named by the token: %s", e.Name())
		}
		info, _ := e.Info()
		if info.Mode().Perm() != 0o700 {
			t.Errorf("directory %s is %v, want 0700", e.Name(), info.Mode().Perm())
		}
		files, _ := os.ReadDir(filepath.Join(dir, e.Name()))
		for _, f := range files {
			fi, _ := f.Info()
			if fi.Mode().Perm() != 0o600 {
				t.Errorf("file %s is %v, want 0600", f.Name(), fi.Mode().Perm())
			}
		}
	}
	// Bob's first GET was not served from Alice's entry.
	if s.requests[1].Get("If-None-Match") != "" {
		t.Error("one token's request used another's cached response")
	}
}

// Over its cap, the directory drops the least recently used files.
func TestDiskCacheEvictsOverItsCap(t *testing.T) {
	c, err := newDiskCache(t.TempDir(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	value := make([]byte, 300)
	old := time.Now().Add(-time.Hour)
	c.Set("a", value)
	_ = os.Chtimes(c.path("a"), old, old)
	c.Set("b", value)
	c.Set("c", value)
	c.Set("d", value) // 1200 > 1000: evict down to 900
	if _, ok := c.Get("a"); ok {
		t.Error("the least recently used entry survived eviction")
	}
	for _, k := range []string{"c", "d"} {
		if _, ok := c.Get(k); !ok {
			t.Errorf("%s was evicted", k)
		}
	}
	if c.measure() > 1000 {
		t.Errorf("directory is %d bytes, over its cap", c.measure())
	}
}

// What cannot be read as a response is a miss, not an error: the cache
// fetches it again.
func TestDiskCacheCorruptEntryIsAMiss(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvCacheDir, dir)
	forgetCaches(t)
	t.Cleanup(func() { forgetCaches(t) })
	s := &etagServer{}
	srv := httptest.NewServer(http.HandlerFunc(s.handler))
	defer srv.Close()

	get(t, cachedHTTPClient(context.Background(), "token-corrupt"), srv.URL+"/x")
	forgetCaches(t)
	tokenDirs, _ := os.ReadDir(dir)
	files, _ := os.ReadDir(filepath.Join(dir, tokenDirs[0].Name()))
	for _, f := range files {
		_ = os.WriteFile(filepath.Join(dir, tokenDirs[0].Name(), f.Name()), []byte("not a response"), 0o600)
	}
	code, body := get(t, cachedHTTPClient(context.Background(), "token-corrupt"), srv.URL+"/x")
	if code != http.StatusOK || body != `{"number": 1}` {
		t.Fatalf("after a corrupt entry: %d %q", code, body)
	}
	if s.requests[1].Get("If-None-Match") != "" {
		t.Error("a corrupt entry was used for a conditional request")
	}
}

// Without EnvCacheDir nothing is written to disk.
func TestNoDiskCacheWithoutTheEnv(t *testing.T) {
	t.Setenv(EnvCacheDir, "")
	forgetCaches(t)
	t.Cleanup(func() { forgetCaches(t) })
	if _, ok := cacheFor("token-memory").(*lruCache); !ok {
		t.Errorf("cache is %T, want the in-memory one alone", cacheFor("token-memory"))
	}
}
