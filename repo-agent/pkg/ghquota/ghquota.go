/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package ghquota builds the GitHub HTTP clients this process uses, with
// the two behaviours that keep a shared personal token inside its 5000
// requests an hour: conditional requests, so unchanged resources cost
// nothing, and a gate that stops calling once GitHub says the budget is
// spent.
//
// Both matter because the limit is per USER, not per component — the API
// server, the controller and every factory subprocess spend the same
// member's budget, and so does the member's own `gh` and `git`.
package ghquota

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/google/go-github/v39/github"
	"github.com/gregjones/httpcache"
	"golang.org/x/oauth2"
	"k8s.io/klog/v2"
)

// The conditional-request caches hold bodies and their ETags. GitHub
// answers an unchanged resource with 304, which costs ZERO quota — the
// difference between polling being a quota problem and being nearly free
// at steady state.
//
// One cache per token. httpcache keys an entry by URL alone and checks
// Vary: Authorization only when it reads one, so in a shared cache two
// members reading the same URL (two boards on one repository) overwrite
// each other's entry: neither ever finds its own ETag, and every read
// goes out unconditional, a paid 200. Per token, a member's boards share
// their entries and members never evict each other. Vary still guards
// against serving one member's body to another.
//
// Bounded: tokenCacheBytes per token, least recently used entries first,
// and maxTokenCaches tokens, least recently used token first, so a
// long-running process does not grow with every URL and member it meets.
//
// In memory, so a restart starts cold, and each process (the API, the
// controller) has its own. Persisting it (redis is already in the
// namespace, and httpcache ships an adapter) is the next move if restarts
// turn out to cost a window.
var (
	tokenCacheBytes = 8 << 20
	maxTokenCaches  = 32
)

var caches = struct {
	sync.Mutex
	order   *list.List // of *stickyCache, front: most recently used
	byToken map[string]*list.Element
}{order: list.New(), byToken: map[string]*list.Element{}}

// cacheFor is the token's conditional-request cache, by its bucket.
func cacheFor(bucket string) *stickyCache {
	caches.Lock()
	defer caches.Unlock()
	if el, ok := caches.byToken[bucket]; ok {
		caches.order.MoveToFront(el)
		return el.Value.(*stickyCache)
	}
	c := &stickyCache{bucket: bucket, Cache: newLRUCache(tokenCacheBytes)}
	caches.byToken[bucket] = caches.order.PushFront(c)
	for caches.order.Len() > maxTokenCaches {
		oldest := caches.order.Back()
		caches.order.Remove(oldest)
		delete(caches.byToken, oldest.Value.(*stickyCache).bucket)
	}
	return c
}

// HTTPClient returns the http.Client for a token: the gate on the wire,
// the conditional-request cache over it, oauth2 on top.
//
// Both layers are load-bearing in that order. oauth2 must be OUTSIDE the
// cache so the cache sees the Authorization header — that is what makes
// GitHub's Vary: Authorization actually partition entries per token;
// inverted, the cache silently shares bodies across members. The gate must
// be UNDER the cache so it reads GitHub's answer first: the cache decides
// whether to forget an entry based on that answer, and it must already
// know the budget is gone when it decides.
func HTTPClient(token string) *http.Client {
	bucket := bucketKey(token)
	cached := httpcache.NewTransport(cacheFor(bucket))
	cached.Transport = &gate{bucket: bucket, base: http.DefaultTransport}
	return &http.Client{Transport: &oauth2.Transport{
		Source: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token}),
		Base:   cached,
	}}
}

// Error is a call this process did not make. GitHub already said the
// token's budget was spent, and nothing gives it back before Until.
type Error struct {
	Until time.Time
}

func (e *Error) Error() string {
	return fmt.Sprintf("github quota spent for this token; not calling until %s (%s away)",
		e.Until.UTC().Format(time.RFC3339), time.Until(e.Until).Round(time.Second))
}

// IsRateLimited reports whether err is this package withholding a call, or
// GitHub refusing one. Callers use it to hold what they already have
// instead of rebuilding into a wall.
func IsRateLimited(err error) bool {
	var gated *Error
	if errors.As(err, &gated) {
		return true
	}
	return ghSaysRateLimited(err)
}

// ResetAt reports when a rate-limit error expects the budget back, and
// whether it could tell. Callers keep serving what they have until then.
func ResetAt(err error) (time.Time, bool) {
	var gated *Error
	if errors.As(err, &gated) {
		return gated.Until, true
	}
	return ghResetAt(err)
}

// ghSaysRateLimited covers the calls that got out before the gate knew:
// go-github turns GitHub's own refusals into these two.
func ghSaysRateLimited(err error) bool {
	var limit *github.RateLimitError
	var abuse *github.AbuseRateLimitError
	return errors.As(err, &limit) || errors.As(err, &abuse)
}

func ghResetAt(err error) (time.Time, bool) {
	var limit *github.RateLimitError
	if errors.As(err, &limit) && !limit.Rate.Reset.IsZero() {
		return limit.Rate.Reset.Time, true
	}
	var abuse *github.AbuseRateLimitError
	if errors.As(err, &abuse) && abuse.RetryAfter != nil {
		return time.Now().Add(*abuse.RetryAfter), true
	}
	return time.Time{}, false
}

// buckets tracks, per token, when this process will next call GitHub.
// Package-level on purpose: clients are built per request, so the
// knowledge that the budget is gone has to outlive them — go-github's own
// pre-check remembers this only within one client, which is why it never
// fires here.
var buckets = struct {
	sync.Mutex
	until map[string]time.Time
}{until: map[string]time.Time{}}

func bucketKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:8])
}

func blocked(bucket string) (time.Time, bool) {
	buckets.Lock()
	defer buckets.Unlock()
	until, ok := buckets.until[bucket]
	if !ok || !time.Now().Before(until) {
		return time.Time{}, false
	}
	return until, true
}

func hold(bucket string, until time.Time, why string) {
	buckets.Lock()
	_, was := buckets.until[bucket]
	buckets.until[bucket] = until
	buckets.Unlock()
	if !was {
		// Once per exhaustion, not once per refused call: the point is to
		// make the window visible in the log, not to fill it.
		klog.Infof("github %s; withholding calls on this token until %s", why, until.UTC().Format(time.RFC3339))
	}
}

func release(bucket string) {
	buckets.Lock()
	defer buckets.Unlock()
	delete(buckets.until, bucket)
}

type gate struct {
	bucket string
	base   http.RoundTripper
}

func (g *gate) RoundTrip(req *http.Request) (*http.Response, error) {
	if until, yes := blocked(g.bucket); yes {
		return nil, &Error{Until: until}
	}
	resp, err := g.base.RoundTrip(req)
	if err != nil || resp == nil {
		return resp, err
	}
	g.observe(resp)
	return resp, nil
}

// observe reads the budget headers GitHub puts on every answer, including
// 304s. Remaining zero is the signal whatever the status: the call that
// spends the last unit says as much as the first one refused, and acting
// on it saves a round of guaranteed 403s.
func (g *gate) observe(resp *http.Response) {
	// Secondary limits (too many concurrent calls — what a per-PR fan-out
	// invites) come back 403 with Retry-After and budget to spare.
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		if secs, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && secs > 0 {
			hold(g.bucket, time.Now().Add(time.Duration(secs)*time.Second), "asked us to slow down")
			return
		}
	}
	remaining, err := strconv.Atoi(resp.Header.Get("X-RateLimit-Remaining"))
	if err != nil {
		return // no budget headers on this answer; nothing to learn
	}
	if remaining > 0 {
		release(g.bucket)
		return
	}
	hold(g.bucket, resetFromHeader(resp), "budget spent")
}

// resetFromHeader trusts GitHub's reset whenever it is in the future. The
// minute fallback covers a header that is missing, unparseable or already
// past: guessing late costs one refused call, guessing zero costs a spin.
func resetFromHeader(resp *http.Response) time.Time {
	secs, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64)
	if err == nil {
		if reset := time.Unix(secs, 0); reset.After(time.Now()) {
			return reset
		}
	}
	return time.Now().Add(time.Minute)
}

// stickyCache is the conditional-request cache with one behaviour changed:
// while the budget is spent, it is frozen — it neither forgets what it
// knows nor learns anything from a refusal.
//
// gregjones/httpcache treats any answer that is not a 200 as a reason to
// drop the entry and then, if the answer is storable, to store the answer
// in its place. A rate-limited request is a 403 with Cache-Control, so
// exhaustion first deletes the ETag that would have made the next window
// nearly free and then caches the refusal over it. Recovery starts cold,
// pays for every body again, and spends the new window the same way: a
// ratchet, not a bad hour. Ordinary eviction (a 404 for a PR that is
// gone) is untouched — this only holds while GitHub is saying no.
//
// The cache is its token's, so it freezes while its own token is held:
// one member's exhaustion leaves another's entries alone.
type stickyCache struct {
	httpcache.Cache
	bucket string
}

func (c *stickyCache) Delete(key string) {
	if _, held := blocked(c.bucket); held {
		return
	}
	c.Cache.Delete(key)
}

func (c *stickyCache) Set(key string, responseBytes []byte) {
	if _, held := blocked(c.bucket); held {
		return
	}
	c.Cache.Set(key, responseBytes)
}
