package ghquota

import (
	"container/list"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// server answers conditional requests the way GitHub does, and hands out
// whatever budget the test tells it to.
type server struct {
	full, conditional, total atomic.Int32
	remaining                atomic.Int32
	resetIn                  atomic.Int64 // seconds; 0 = no reset header
	retryAfter               atomic.Int32
}

func (s *server) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.total.Add(1)
		remaining := s.remaining.Load()
		w.Header().Set("Vary", "Accept, Authorization")
		w.Header().Set("Cache-Control", "private, max-age=0") // force revalidation
		w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(int(remaining)))
		if secs := s.resetIn.Load(); secs != 0 {
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(time.Duration(secs)*time.Second).Unix(), 10))
		}
		if after := s.retryAfter.Load(); after != 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(after)))
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if remaining <= 0 {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
			return
		}
		w.Header().Set("ETag", `"abc123"`)
		if r.Header.Get("If-None-Match") == `"abc123"` {
			s.conditional.Add(1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		s.full.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"number":7,"title":"cached"}]`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// get issues one request through the full transport stack and reports the
// body it got, or the error the gate raised.
func get(t *testing.T, token, url string) (string, error) {
	t.Helper()
	resp, err := HTTPClient(token).Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return string(body), nil
}

// fresh isolates a test from every other one: the cache and the buckets
// are package state on purpose (clients are built per request), so a test
// that leaves a token held would block the next.
func fresh(t *testing.T) {
	t.Helper()
	reset := func() {
		buckets.Lock()
		buckets.until = map[string]time.Time{}
		buckets.Unlock()
		caches.Lock()
		caches.order = list.New()
		caches.byToken = map[string]*list.Element{}
		caches.Unlock()
	}
	reset()
	t.Cleanup(reset)
}

// The whole point of the cache: a second read of an unchanged resource
// asks with If-None-Match and is answered 304, which GitHub does not
// charge for.
func TestUnchangedResourceIsRevalidatedNotRefetched(t *testing.T) {
	fresh(t)
	srv := &server{}
	srv.remaining.Store(100)
	url := srv.start(t).URL + "/repos/o/r/issues"

	for i := 0; i < 3; i++ {
		body, err := get(t, "tok-revalidate", url)
		if err != nil {
			t.Fatalf("request %d failed: %v", i, err)
		}
		if body == "" {
			t.Fatalf("request %d served no body", i)
		}
	}
	if srv.full.Load() != 1 || srv.conditional.Load() != 2 {
		t.Fatalf("want 1 paid + 2 conditional, got %d paid / %d conditional", srv.full.Load(), srv.conditional.Load())
	}
}

// Two members share the process but not each other's answers — the cache
// sits under oauth2 so Vary: Authorization can partition it.
func TestOneTokenNeverRidesAnothersCache(t *testing.T) {
	fresh(t)
	srv := &server{}
	srv.remaining.Store(100)
	url := srv.start(t).URL + "/repos/o/r/private"

	if _, err := get(t, "tok-alice", url); err != nil {
		t.Fatalf("alice: %v", err)
	}
	if _, err := get(t, "tok-bob", url); err != nil {
		t.Fatalf("bob: %v", err)
	}
	if srv.full.Load() != 2 {
		t.Fatalf("bob must pay for his own copy, got %d paid requests", srv.full.Load())
	}
}

// Once GitHub says the budget is gone, the process stops asking. The
// knowledge has to outlive the client that learned it — clients are built
// per request here, which is exactly why go-github's own pre-check never
// fires.
func TestSpentBudgetStopsTheNextCallBeforeItLeaves(t *testing.T) {
	fresh(t)
	srv := &server{}
	srv.remaining.Store(0)
	srv.resetIn.Store(3600)
	url := srv.start(t).URL + "/repos/o/r/issues"

	if _, err := get(t, "tok-spent", url); err != nil {
		t.Fatalf("the first call still goes out: %v", err)
	}
	_, err := get(t, "tok-spent", url)
	if err == nil {
		t.Fatal("the second call must be refused here, not by GitHub")
	}
	if !IsRateLimited(err) {
		t.Fatalf("refusal must be recognisable as a rate limit, got %v", err)
	}
	until, ok := ResetAt(err)
	if !ok || time.Until(until) < 50*time.Minute {
		t.Fatalf("refusal must carry GitHub's reset, got %v (ok=%v)", until, ok)
	}
	if srv.total.Load() != 1 {
		t.Fatalf("only the first call may reach GitHub, got %d", srv.total.Load())
	}
	// A different member's budget is their own.
	if _, err := get(t, "tok-solvent", url); err != nil {
		t.Fatalf("another token must not inherit the block: %v", err)
	}
}

// Secondary limits are a different refusal: budget to spare, but too many
// calls at once — which is what a per-PR fan-out invites.
func TestRetryAfterIsHonouredEvenWithBudgetLeft(t *testing.T) {
	fresh(t)
	srv := &server{}
	srv.remaining.Store(4000)
	srv.retryAfter.Store(60)
	url := srv.start(t).URL + "/repos/o/r/issues"

	if _, err := get(t, "tok-slow-down", url); err != nil {
		t.Fatalf("the first call still goes out: %v", err)
	}
	if _, err := get(t, "tok-slow-down", url); !IsRateLimited(err) {
		t.Fatalf("Retry-After must hold the next call, got %v", err)
	}
}

// The ratchet this package exists to break: httpcache drops an entry
// whenever a request for it does not come back 200, so a rate-limited
// 403 throws away the ETag that would have made the next window free.
// After the block lifts, the retry must still revalidate — never pay for
// a body it already had.
func TestExhaustionDoesNotThrowAwayTheEtag(t *testing.T) {
	fresh(t)
	srv := &server{}
	srv.remaining.Store(100)
	url := srv.start(t).URL + "/repos/o/r/issues"

	if _, err := get(t, "tok-ratchet", url); err != nil {
		t.Fatalf("priming the cache: %v", err)
	}
	// The budget runs out, and the reset is a blink away.
	srv.remaining.Store(0)
	srv.resetIn.Store(1)
	if _, err := get(t, "tok-ratchet", url); err != nil {
		t.Fatalf("the call that discovers exhaustion still returns a response: %v", err)
	}
	if _, err := get(t, "tok-ratchet", url); !IsRateLimited(err) {
		t.Fatalf("calls must be withheld while the budget is gone, got %v", err)
	}

	// Budget back.
	time.Sleep(1200 * time.Millisecond)
	srv.remaining.Store(100)
	before := srv.full.Load()
	body, err := get(t, "tok-ratchet", url)
	if err != nil {
		t.Fatalf("after the reset: %v", err)
	}
	if body == "" {
		t.Fatal("the cached body must still be served on the 304")
	}
	if srv.full.Load() != before {
		t.Fatal("recovery re-paid for a body it already had: the 403 evicted the ETag")
	}
}

// Ordinary eviction is untouched: a resource that is gone must not be
// remembered just because a token is out of budget. The freeze is the
// cache's own token's: another member's exhaustion leaves it alone.
func TestNormalEvictionSurvivesTheChange(t *testing.T) {
	fresh(t)
	cache := &stickyCache{Cache: memory{}, bucket: "some-bucket"}
	cache.Set("k", []byte("v"))
	cache.Delete("k")
	if _, ok := cache.Get("k"); ok {
		t.Fatal("with budget in hand, Delete must delete")
	}

	cache.Set("k", []byte("v"))
	hold("another-bucket", time.Now().Add(time.Hour), "test")
	cache.Delete("k")
	if _, ok := cache.Get("k"); ok {
		t.Fatal("another token's exhaustion must not freeze this token's cache")
	}

	cache.Set("k", []byte("v"))
	hold("some-bucket", time.Now().Add(time.Hour), "test")
	cache.Delete("k")
	if _, ok := cache.Get("k"); !ok {
		t.Fatal("while its token's budget is gone, the cache must refuse to forget")
	}
}

type memory map[string][]byte

func (m memory) Get(key string) ([]byte, bool) { v, ok := m[key]; return v, ok }
func (m memory) Set(key string, v []byte)      { m[key] = v }
func (m memory) Delete(key string)             { delete(m, key) }

// Two members' boards on one repository read the same URLs. Each must
// keep its own ETag: a member's repeat read revalidates (304) however
// the other's reads interleave with it. Keyed by URL alone, each fetch
// overwrote the other's entry, Vary: Authorization then failed to match,
// and every read went out unconditional — a paid 200 each time.
func TestTwoTokensOnOneURLEachKeepTheirETag(t *testing.T) {
	fresh(t)
	srv := &server{}
	srv.remaining.Store(100)
	url := srv.start(t).URL + "/repos/o/r/issues"

	for i := 0; i < 3; i++ {
		for _, token := range []string{"tok-thrash-alice", "tok-thrash-bob"} {
			if _, err := get(t, token, url); err != nil {
				t.Fatalf("%s, round %d: %v", token, i, err)
			}
		}
	}
	if srv.full.Load() != 2 || srv.conditional.Load() != 4 {
		t.Fatalf("want 2 paid (one each) + 4 conditional, got %d paid / %d conditional", srv.full.Load(), srv.conditional.Load())
	}
}

// One member's clients share their cache: a second client for the same
// token (a second board, a later request) revalidates what the first
// fetched.
func TestOneTokenSharesItsCacheAcrossClients(t *testing.T) {
	fresh(t)
	srv := &server{}
	srv.remaining.Store(100)
	url := srv.start(t).URL + "/repos/o/r/issues"

	for i := 0; i < 2; i++ {
		resp, err := HTTPClient("tok-shared").Get(url)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
	}
	if srv.full.Load() != 1 || srv.conditional.Load() != 1 {
		t.Fatalf("want 1 paid + 1 conditional, got %d paid / %d conditional", srv.full.Load(), srv.conditional.Load())
	}
}

// A token's cache holds at most its bound, forgetting the least recently
// used entries first; an entry bigger than the bound is not kept.
func TestTokenCacheIsBounded(t *testing.T) {
	c := newLRUCache(10)
	c.Set("a", []byte("1234"))
	c.Set("b", []byte("1234"))
	c.Get("a") // a is now the most recent
	c.Set("c", []byte("1234"))
	if _, ok := c.Get("b"); ok {
		t.Error("b, the least recently used, was kept past the bound")
	}
	for _, k := range []string{"a", "c"} {
		if _, ok := c.Get(k); !ok {
			t.Errorf("%s was forgotten", k)
		}
	}
	c.Set("a", []byte("12")) // replacing an entry re-counts it
	if c.bytes != 6 {
		t.Errorf("bytes = %d after replacing a, want 6", c.bytes)
	}
	c.Set("huge", []byte("12345678901"))
	if _, ok := c.Get("huge"); ok || c.bytes != 6 {
		t.Errorf("an entry over the bound was kept (bytes %d)", c.bytes)
	}
}

// The process keeps at most maxTokenCaches token caches, dropping the
// least recently used token's first.
func TestTokenCachesAreBounded(t *testing.T) {
	fresh(t)
	defer func(n int) { maxTokenCaches = n }(maxTokenCaches)
	maxTokenCaches = 2
	a, b := cacheFor("a"), cacheFor("b")
	if cacheFor("a") != a {
		t.Fatal("a token's cache was not reused")
	}
	cacheFor("c") // b is the least recently used
	if cacheFor("a") != a {
		t.Error("a, recently used, was dropped")
	}
	if cacheFor("b") == b {
		t.Error("b, the least recently used, was kept past the bound")
	}
}
