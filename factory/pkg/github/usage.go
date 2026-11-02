package github

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// What this process asked GitHub, and what it cost: counted where a request
// leaves for the network, under the response cache (cachedHTTPClient), so a
// revalidation GitHub answers 304 - free - is told apart from a 200 or a
// write, which count against the token's rate limit.
//
// Each request is counted in a series: the token it went as (by a short
// hash, or the name the process gave it - "watcher", a bot's login), the
// component that asked (WithComponent: the watch's issue scan, its graph
// sweep, a task's run...), and the endpoint (the method and the path with
// its numbers, SHAs and repository templated). The rate limit's own headers
// on each response are kept per token too: the limit is the token's, so the
// counts are set against what GitHub says that token has left.

// Outcomes of a request, as GitHub bills them.
const (
	// OutcomeFree is a conditional GET answered 304 Not Modified: no rate
	// limit is spent.
	OutcomeFree = "free"
	// OutcomeRead is a GET (or HEAD) answered with anything but 304: one
	// request of the rate limit.
	OutcomeRead = "read"
	// OutcomeWrite is a POST, PATCH, PUT or DELETE other than a GraphQL
	// query: one request of the rate limit.
	OutcomeWrite = "write"
	// OutcomeGraphQL is a POST to the GraphQL endpoint, which has its own
	// limit (in points, not requests).
	OutcomeGraphQL = "graphql"
	// OutcomeError is a request that got no response (a network error).
	OutcomeError = "error"
)

// EnvUsageComponent names the component of a process's requests that no
// context names (WithComponent): a task's child process, by its task.
const EnvUsageComponent = "FACTORY_GITHUB_COMPONENT"

// componentOther is the component of a request no context, nor the
// process (EnvUsageComponent), names.
const componentOther = "other"

type componentKey struct{}

// WithComponent tags ctx's GitHub requests as component's: the watch's
// subcontrollers each run on a context of their own name.
func WithComponent(ctx context.Context, component string) context.Context {
	return context.WithValue(ctx, componentKey{}, component)
}

func componentOf(ctx context.Context) string {
	if c, ok := ctx.Value(componentKey{}).(string); ok && c != "" {
		return c
	}
	return processComponent
}

// processComponent is the process's own component, read once at start.
var processComponent = func() string {
	if c := strings.TrimSpace(os.Getenv(EnvUsageComponent)); c != "" {
		return c
	}
	return componentOther
}()

// TokenID is a token's id in the counts: a short hash, never the token.
func TokenID(token string) string {
	sum := sha256.Sum256([]byte(token))
	return "token-" + hex.EncodeToString(sum[:4])
}

// NameToken names token in the counts (by its id): "watcher", a bot's
// login. A token named twice keeps the last name.
func NameToken(token, name string) {
	if token == "" || name == "" {
		return
	}
	processUsage.name(TokenID(token), name)
}

// Series is one token's, one component's requests to one endpoint.
type Series struct {
	Token     string         `json:"token"`
	Component string         `json:"component"`
	Endpoint  string         `json:"endpoint"`
	Total     int            `json:"total"`
	Charged   int            `json:"charged"`
	Outcomes  map[string]int `json:"outcomes"`
	// Statuses counts the HTTP statuses answered, e.g. 403 when the rate
	// limit ran out.
	Statuses map[int]int `json:"statuses,omitempty"`
}

func (s Series) key() string { return s.Token + "\x00" + s.Component + "\x00" + s.Endpoint }

// Usage is the counts of one view of the series: by endpoint, by
// component, or by token.
type Usage struct {
	Name     string         `json:"name"`
	Total    int            `json:"total"`
	Charged  int            `json:"charged"`
	Outcomes map[string]int `json:"outcomes"`
	Statuses map[int]int    `json:"statuses,omitempty"`
}

// RateLimit is what GitHub's last response said of one rate limit
// resource (X-RateLimit-Resource: core, graphql, search...).
type RateLimit struct {
	Limit     int       `json:"limit"`
	Remaining int       `json:"remaining"`
	Used      int       `json:"used"`
	Reset     time.Time `json:"reset"`
	Seen      time.Time `json:"seen"`
}

// UsageSnapshot is the counts since Since: the series, and their views,
// each most charged first.
type UsageSnapshot struct {
	Since      time.Time      `json:"since"`
	At         time.Time      `json:"at"`
	Total      int            `json:"total"`
	Charged    int            `json:"charged"`
	Outcomes   map[string]int `json:"outcomes"`
	Tokens     []Usage        `json:"tokens"`
	Components []Usage        `json:"components"`
	Endpoints  []Usage        `json:"endpoints"`
	Series     []Series       `json:"series"`
	// RateLimits is each token's (by name) rate limits, by resource.
	RateLimits map[string]map[string]RateLimit `json:"rateLimits,omitempty"`
	// TokenNames names token ids.
	TokenNames map[string]string `json:"tokenNames,omitempty"`

	rate map[string]map[string]RateLimit // RateLimits by token id
}

// usageStats is a set of counts; one is the process's (processUsage).
type usageStats struct {
	mu     sync.Mutex
	since  time.Time
	series map[string]*Series
	rate   map[string]map[string]RateLimit // token id → resource →
	names  map[string]string               // token id → name
}

func newUsageStats(now time.Time) *usageStats {
	return &usageStats{since: now, series: map[string]*Series{}, rate: map[string]map[string]RateLimit{}, names: map[string]string{}}
}

var processUsage = newUsageStats(time.Now())

// ProcessUsage is this process's GitHub usage since it started, and its
// children's it read (IngestUsage).
func ProcessUsage() UsageSnapshot { return processUsage.snapshot() }

func (u *usageStats) name(id, name string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.names[id] = name
}

func (u *usageStats) record(token, component, endpoint, outcome string, status int, h http.Header, now time.Time) {
	s := Series{Token: token, Component: component, Endpoint: endpoint, Total: 1, Outcomes: map[string]int{outcome: 1}}
	if charged(outcome) {
		s.Charged = 1
	}
	if status != 0 {
		s.Statuses = map[int]int{status: 1}
	}
	var rate map[string]RateLimit
	if res := h.Get("X-RateLimit-Resource"); h != nil && res != "" {
		rl := RateLimit{Seen: now}
		rl.Limit, _ = strconv.Atoi(h.Get("X-RateLimit-Limit"))
		rl.Remaining, _ = strconv.Atoi(h.Get("X-RateLimit-Remaining"))
		rl.Used, _ = strconv.Atoi(h.Get("X-RateLimit-Used"))
		if reset, err := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			rl.Reset = time.Unix(reset, 0).UTC()
		}
		rate = map[string]RateLimit{res: rl}
	}
	u.add([]Series{s}, map[string]map[string]RateLimit{token: rate}, nil)
}

// add adds series to the counts, and keeps the rate limits seen later than
// those kept, and the token names.
func (u *usageStats) add(series []Series, rate map[string]map[string]RateLimit, names map[string]string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, s := range series {
		e := u.series[s.key()]
		if e == nil {
			e = &Series{Token: s.Token, Component: s.Component, Endpoint: s.Endpoint, Outcomes: map[string]int{}, Statuses: map[int]int{}}
			u.series[s.key()] = e
		}
		e.Total += s.Total
		e.Charged += s.Charged
		for k, v := range s.Outcomes {
			e.Outcomes[k] += v
		}
		for k, v := range s.Statuses {
			e.Statuses[k] += v
		}
	}
	for token, byRes := range rate {
		for res, rl := range byRes {
			if u.rate[token] == nil {
				u.rate[token] = map[string]RateLimit{}
			}
			if rl.Seen.After(u.rate[token][res].Seen) || u.rate[token][res].Seen.IsZero() {
				u.rate[token][res] = rl
			}
		}
	}
	for id, n := range names {
		if _, ok := u.names[id]; !ok {
			u.names[id] = n
		}
	}
}

// charged reports whether an outcome counts against the REST rate limit.
func charged(outcome string) bool { return outcome == OutcomeRead || outcome == OutcomeWrite }

func (u *usageStats) snapshot() UsageSnapshot {
	u.mu.Lock()
	defer u.mu.Unlock()
	series := make([]Series, 0, len(u.series))
	for _, s := range u.series {
		series = append(series, s.clone())
	}
	rate := map[string]map[string]RateLimit{}
	for t, byRes := range u.rate {
		rate[t] = map[string]RateLimit{}
		for r, rl := range byRes {
			rate[t][r] = rl
		}
	}
	names := map[string]string{}
	for k, v := range u.names {
		names[k] = v
	}
	return newSnapshot(u.since, time.Now(), series, rate, names)
}

func (s Series) clone() Series {
	c := s
	c.Outcomes = map[string]int{}
	for k, v := range s.Outcomes {
		c.Outcomes[k] = v
	}
	c.Statuses = map[int]int{}
	for k, v := range s.Statuses {
		c.Statuses[k] = v
	}
	return c
}

// newSnapshot is series' snapshot: their totals and views, the rate limits
// by token name.
func newSnapshot(since, at time.Time, series []Series, rate map[string]map[string]RateLimit, names map[string]string) UsageSnapshot {
	nameOf := func(id string) string {
		if n := names[id]; n != "" {
			return n
		}
		return id
	}
	s := UsageSnapshot{Since: since, At: at, Outcomes: map[string]int{}, Series: series, TokenNames: names, RateLimits: map[string]map[string]RateLimit{}, rate: rate}
	tokens, components, endpoints := map[string]*Usage{}, map[string]*Usage{}, map[string]*Usage{}
	for _, e := range series {
		s.Total += e.Total
		s.Charged += e.Charged
		for k, v := range e.Outcomes {
			s.Outcomes[k] += v
		}
		for view, key := range map[*map[string]*Usage]string{&tokens: nameOf(e.Token), &components: e.Component, &endpoints: e.Endpoint} {
			u := (*view)[key]
			if u == nil {
				u = &Usage{Name: key, Outcomes: map[string]int{}, Statuses: map[int]int{}}
				(*view)[key] = u
			}
			u.Total += e.Total
			u.Charged += e.Charged
			for k, v := range e.Outcomes {
				u.Outcomes[k] += v
			}
			for k, v := range e.Statuses {
				u.Statuses[k] += v
			}
		}
	}
	s.Tokens, s.Components, s.Endpoints = sortedUsage(tokens), sortedUsage(components), sortedUsage(endpoints)
	sort.Slice(s.Series, func(i, j int) bool {
		return less(s.Series[i].Charged, s.Series[j].Charged, s.Series[i].Total, s.Series[j].Total, s.Series[i].key(), s.Series[j].key())
	})
	for t, byRes := range rate {
		s.RateLimits[nameOf(t)] = byRes
	}
	return s
}

func sortedUsage(m map[string]*Usage) []Usage {
	out := make([]Usage, 0, len(m))
	for _, u := range m {
		out = append(out, *u)
	}
	sort.Slice(out, func(i, j int) bool {
		return less(out[i].Charged+out[i].Outcomes[OutcomeGraphQL], out[j].Charged+out[j].Outcomes[OutcomeGraphQL], out[i].Total, out[j].Total, out[i].Name, out[j].Name)
	})
	return out
}

// less orders most charged first, then most requests, then by name.
func less(ci, cj, ti, tj int, ni, nj string) bool {
	if ci != cj {
		return ci > cj
	}
	if ti != tj {
		return ti > tj
	}
	return ni < nj
}

// Minus is what was counted after an earlier snapshot, prev: the series
// that grew, and the latest rate limits.
func (s UsageSnapshot) Minus(prev UsageSnapshot) UsageSnapshot {
	before := map[string]Series{}
	for _, e := range prev.Series {
		before[e.key()] = e
	}
	var series []Series
	for _, e := range s.Series {
		d := diff(e, before[e.key()])
		if d.Total != 0 {
			series = append(series, d)
		}
	}
	return newSnapshot(prev.At, s.At, series, s.rate, s.TokenNames)
}

func diff(e, b Series) Series {
	d := Series{Token: e.Token, Component: e.Component, Endpoint: e.Endpoint, Total: e.Total - b.Total, Charged: e.Charged - b.Charged, Outcomes: map[string]int{}, Statuses: map[int]int{}}
	for k, v := range e.Outcomes {
		if n := v - b.Outcomes[k]; n != 0 {
			d.Outcomes[k] = n
		}
	}
	for k, v := range e.Statuses {
		if n := v - b.Statuses[k]; n != 0 {
			d.Statuses[k] = n
		}
	}
	return d
}

// Summary is the snapshot in one log line: totals, each token's core
// limit left and charges, then the top components and endpoints by what
// they were charged.
func (s UsageSnapshot) Summary(top int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "charged %d (reads %d, writes %d), free 304s %d, graphql %d, errors %d",
		s.Charged, s.Outcomes[OutcomeRead], s.Outcomes[OutcomeWrite], s.Outcomes[OutcomeFree], s.Outcomes[OutcomeGraphQL], s.Outcomes[OutcomeError])
	if len(s.Tokens) > 0 {
		b.WriteString("; tokens:")
		for i, t := range s.Tokens {
			if i > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, " %s %d", t.Name, t.Charged)
			if rl, ok := s.RateLimits[t.Name]["core"]; ok {
				fmt.Fprintf(&b, " (%d of %d left, resets %s)", rl.Remaining, rl.Limit, rl.Reset.Format("15:04:05Z"))
			}
		}
	}
	writeTop(&b, "components", s.Components, top)
	writeTop(&b, "top", s.Endpoints, top)
	return b.String()
}

func writeTop(b *strings.Builder, label string, us []Usage, top int) {
	n := 0
	for _, e := range us {
		if n == top {
			break
		}
		if e.Charged == 0 && e.Outcomes[OutcomeGraphQL] == 0 {
			continue
		}
		if n == 0 {
			fmt.Fprintf(b, "; %s:", label)
		} else {
			b.WriteString(",")
		}
		fmt.Fprintf(b, " %s %d", e.Name, e.Charged+e.Outcomes[OutcomeGraphQL])
		if f := e.Outcomes[OutcomeFree]; f > 0 {
			fmt.Fprintf(b, " (+%d free)", f)
		}
		n++
	}
}

// countingTransport counts each request it sends as token's (by id):
// under the cache, it sees what GitHub answered.
type countingTransport struct {
	next  http.RoundTripper
	token string
	stats *usageStats
}

func (t countingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.next.RoundTrip(req)
	stats := t.stats
	if stats == nil {
		stats = processUsage
	}
	endpoint := Endpoint(req.Method, req.URL.Path, req.URL.RawQuery)
	component := componentOf(req.Context())
	if err != nil {
		stats.record(t.token, component, endpoint, OutcomeError, 0, nil, time.Now())
		return resp, err
	}
	stats.record(t.token, component, endpoint, outcome(req, resp.StatusCode), resp.StatusCode, resp.Header, time.Now())
	return resp, err
}

func outcome(req *http.Request, status int) string {
	switch {
	case req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/graphql"):
		return OutcomeGraphQL
	case req.Method == http.MethodGet || req.Method == http.MethodHead:
		if status == http.StatusNotModified {
			return OutcomeFree
		}
		return OutcomeRead
	default:
		return OutcomeWrite
	}
}

var (
	reposPrefix = regexp.MustCompile(`^/repos/[^/]+/[^/]+`)
	numberSeg   = regexp.MustCompile(`^[0-9]+$`)
	shaSeg      = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
)

// Endpoint is a request's endpoint: its method, its path with the
// repository, numbers and SHAs templated, and its query's keys (not their
// values), e.g. "GET /repos/{r}/issues?creator,direction,per_page,sort,state".
// The page and per_page values are left out with the rest: a list read page
// by page is one endpoint.
func Endpoint(method, path, rawQuery string) string {
	path = reposPrefix.ReplaceAllString(path, "/repos/{r}")
	segs := strings.Split(path, "/")
	for i, s := range segs {
		switch {
		case numberSeg.MatchString(s):
			segs[i] = "{n}"
		case shaSeg.MatchString(s) && strings.ContainsAny(s, "0123456789"):
			segs[i] = "{sha}"
		}
	}
	e := method + " " + strings.Join(segs, "/")
	if rawQuery == "" {
		return e
	}
	var keys []string
	for _, kv := range strings.Split(rawQuery, "&") {
		k, _, _ := strings.Cut(kv, "=")
		if k != "" && k != "page" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	keys = compact(keys)
	if len(keys) == 0 {
		return e
	}
	return e + "?" + strings.Join(keys, ",")
}

func compact(s []string) []string {
	out := s[:0]
	for i, v := range s {
		if i == 0 || v != s[i-1] {
			out = append(out, v)
		}
	}
	return out
}
