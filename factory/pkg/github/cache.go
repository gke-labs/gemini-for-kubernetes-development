package github

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"sync"

	"github.com/gregjones/httpcache"
	"golang.org/x/oauth2"
)

// maxCachedResponses bounds one token's cache: the responses it keeps for
// conditional requests, least recently used dropped first. A watch or
// the overseer's loops read a few hundred URLs; this leaves room.
const maxCachedResponses = 2000

// tokenCaches are the process's response caches, one per token (by its
// hash): a GET repeated with the same token goes out conditional
// (If-None-Match), and GitHub's 304 costs no rate limit. Tokens never
// share one: a response can differ by who asks (what they may see, a
// pending review only its author sees), and httpcache keys an entry by
// URL alone.
var (
	tokenCachesMu sync.Mutex
	tokenCaches   = map[string]*lruCache{}
)

// cacheFor is token's cache, made on first use.
func cacheFor(token string) *lruCache {
	sum := sha256.Sum256([]byte(token))
	key := hex.EncodeToString(sum[:])
	tokenCachesMu.Lock()
	defer tokenCachesMu.Unlock()
	c, ok := tokenCaches[key]
	if !ok {
		c = newLRUCache(maxCachedResponses)
		tokenCaches[key] = c
	}
	return c
}

// cachedHTTPClient is an HTTP client that authenticates as token and
// caches its GETs in token's cache: oauth2 sets the Authorization header,
// then the cache sends a conditional request under it. Anything but a GET
// or HEAD (a GraphQL POST, a write) passes through. The base transport is
// ctx's oauth2.HTTPClient's, if it has one, as oauth2.NewClient would use.
func cachedHTTPClient(ctx context.Context, token string) *http.Client {
	base := http.DefaultTransport
	if hc, ok := ctx.Value(oauth2.HTTPClient).(*http.Client); ok && hc != nil && hc.Transport != nil {
		base = hc.Transport
	}
	return &http.Client{Transport: &oauth2.Transport{
		Source: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token}),
		Base: revalidate{&httpcache.Transport{
			Transport: base, Cache: cacheFor(token), MarkCachedResponses: true,
		}},
	}}
}

// revalidate asks the cache to check every GET with GitHub: GitHub's
// responses say max-age=60, which would let a cached one stand for a
// minute, so a read after a write (a comment posted, a branch pushed)
// could see the state before it. With max-age=0 on the request the cache
// always sends the conditional request; an unchanged resource is still a
// free 304.
type revalidate struct{ next http.RoundTripper }

func (t revalidate) RoundTrip(req *http.Request) (*http.Response, error) {
	if (req.Method == http.MethodGet || req.Method == http.MethodHead) && req.Header.Get("Cache-Control") == "" {
		req = req.Clone(req.Context())
		req.Header.Set("Cache-Control", "max-age=0")
	}
	return t.next.RoundTrip(req)
}

// lruCache is an httpcache.Cache keeping at most limit responses, the
// least recently used dropped first.
type lruCache struct {
	mu      sync.Mutex
	limit   int
	order   *list.List // front: most recently used
	entries map[string]*list.Element
}

type lruEntry struct {
	key   string
	value []byte
}

func newLRUCache(limit int) *lruCache {
	return &lruCache{limit: limit, order: list.New(), entries: map[string]*list.Element{}}
}

func (c *lruCache) Get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(el)
	return el.Value.(*lruEntry).value, true
}

func (c *lruCache) Set(key string, value []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[key]; ok {
		el.Value.(*lruEntry).value = value
		c.order.MoveToFront(el)
		return
	}
	c.entries[key] = c.order.PushFront(&lruEntry{key: key, value: value})
	for c.order.Len() > c.limit {
		oldest := c.order.Back()
		c.order.Remove(oldest)
		delete(c.entries, oldest.Value.(*lruEntry).key)
	}
}

func (c *lruCache) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[key]; ok {
		c.order.Remove(el)
		delete(c.entries, key)
	}
}

// Len is how many responses the cache keeps.
func (c *lruCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}
