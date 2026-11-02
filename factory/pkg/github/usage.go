package github

import (
	"fmt"
	"net/http"
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
// write, which count against the token's rate limit. Requests are grouped
// by endpoint: the method and the path with its numbers, SHAs and
// repository templated ("GET /repos/{r}/issues/{n}/timeline"). The rate
// limit's own headers on each response are kept too, so the counts can be
// set against what GitHub says is left.

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

// EndpointUsage is one endpoint's counts.
type EndpointUsage struct {
	Endpoint string         `json:"endpoint"`
	Total    int            `json:"total"`
	Charged  int            `json:"charged"`
	Outcomes map[string]int `json:"outcomes"`
	// Statuses counts the HTTP statuses answered, e.g. 403 when the rate
	// limit ran out.
	Statuses map[int]int `json:"statuses,omitempty"`
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

// UsageSnapshot is the process's counts since Since.
type UsageSnapshot struct {
	Since     time.Time            `json:"since"`
	At        time.Time            `json:"at"`
	Total     int                  `json:"total"`
	Charged   int                  `json:"charged"`
	Outcomes  map[string]int       `json:"outcomes"`
	Endpoints []EndpointUsage      `json:"endpoints"` // most charged first
	RateLimit map[string]RateLimit `json:"rateLimit,omitempty"`
}

// usageStats is a set of counts; one is the process's (processUsage).
type usageStats struct {
	mu        sync.Mutex
	since     time.Time
	endpoints map[string]*EndpointUsage
	rate      map[string]RateLimit
}

func newUsageStats(now time.Time) *usageStats {
	return &usageStats{since: now, endpoints: map[string]*EndpointUsage{}, rate: map[string]RateLimit{}}
}

var processUsage = newUsageStats(time.Now())

// Usage is this process's GitHub usage since it started.
func Usage() UsageSnapshot { return processUsage.snapshot() }

func (u *usageStats) record(endpoint, outcome string, status int, h http.Header, now time.Time) {
	u.mu.Lock()
	defer u.mu.Unlock()
	e := u.endpoints[endpoint]
	if e == nil {
		e = &EndpointUsage{Endpoint: endpoint, Outcomes: map[string]int{}, Statuses: map[int]int{}}
		u.endpoints[endpoint] = e
	}
	e.Total++
	e.Outcomes[outcome]++
	if charged(outcome) {
		e.Charged++
	}
	if status != 0 {
		e.Statuses[status]++
	}
	if h == nil {
		return
	}
	if res := h.Get("X-RateLimit-Resource"); res != "" {
		rl := RateLimit{Seen: now}
		rl.Limit, _ = strconv.Atoi(h.Get("X-RateLimit-Limit"))
		rl.Remaining, _ = strconv.Atoi(h.Get("X-RateLimit-Remaining"))
		rl.Used, _ = strconv.Atoi(h.Get("X-RateLimit-Used"))
		if reset, err := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			rl.Reset = time.Unix(reset, 0).UTC()
		}
		u.rate[res] = rl
	}
}

// charged reports whether an outcome counts against the REST rate limit.
func charged(outcome string) bool { return outcome == OutcomeRead || outcome == OutcomeWrite }

func (u *usageStats) snapshot() UsageSnapshot {
	u.mu.Lock()
	defer u.mu.Unlock()
	s := UsageSnapshot{Since: u.since, At: time.Now(), Outcomes: map[string]int{}, RateLimit: map[string]RateLimit{}}
	for _, e := range u.endpoints {
		c := EndpointUsage{Endpoint: e.Endpoint, Total: e.Total, Charged: e.Charged, Outcomes: map[string]int{}, Statuses: map[int]int{}}
		for k, v := range e.Outcomes {
			c.Outcomes[k] = v
			s.Outcomes[k] += v
		}
		for k, v := range e.Statuses {
			c.Statuses[k] = v
		}
		s.Total += e.Total
		s.Charged += e.Charged
		s.Endpoints = append(s.Endpoints, c)
	}
	sortEndpoints(s.Endpoints)
	for k, v := range u.rate {
		s.RateLimit[k] = v
	}
	return s
}

func sortEndpoints(es []EndpointUsage) {
	sort.Slice(es, func(i, j int) bool {
		if es[i].Charged != es[j].Charged {
			return es[i].Charged > es[j].Charged
		}
		if es[i].Total != es[j].Total {
			return es[i].Total > es[j].Total
		}
		return es[i].Endpoint < es[j].Endpoint
	})
}

// Minus is what was counted after an earlier snapshot, prev: the counts
// that grew, and the latest rate limits.
func (s UsageSnapshot) Minus(prev UsageSnapshot) UsageSnapshot {
	before := map[string]EndpointUsage{}
	for _, e := range prev.Endpoints {
		before[e.Endpoint] = e
	}
	d := UsageSnapshot{Since: prev.At, At: s.At, Outcomes: map[string]int{}, RateLimit: s.RateLimit}
	for _, e := range s.Endpoints {
		b := before[e.Endpoint]
		de := EndpointUsage{Endpoint: e.Endpoint, Total: e.Total - b.Total, Charged: e.Charged - b.Charged, Outcomes: map[string]int{}, Statuses: map[int]int{}}
		if de.Total == 0 {
			continue
		}
		for k, v := range e.Outcomes {
			if n := v - b.Outcomes[k]; n != 0 {
				de.Outcomes[k] = n
				d.Outcomes[k] += n
			}
		}
		for k, v := range e.Statuses {
			if n := v - b.Statuses[k]; n != 0 {
				de.Statuses[k] = n
			}
		}
		d.Total += de.Total
		d.Charged += de.Charged
		d.Endpoints = append(d.Endpoints, de)
	}
	sortEndpoints(d.Endpoints)
	return d
}

// Summary is the snapshot in one log line: totals, the core limit left,
// and the top endpoints by what they were charged.
func (s UsageSnapshot) Summary(top int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "charged %d (reads %d, writes %d), free 304s %d, graphql %d, errors %d",
		s.Charged, s.Outcomes[OutcomeRead], s.Outcomes[OutcomeWrite], s.Outcomes[OutcomeFree], s.Outcomes[OutcomeGraphQL], s.Outcomes[OutcomeError])
	if rl, ok := s.RateLimit["core"]; ok {
		fmt.Fprintf(&b, "; core limit: %d of %d left, resets %s", rl.Remaining, rl.Limit, rl.Reset.Format("15:04:05Z"))
	}
	n := 0
	for _, e := range s.Endpoints {
		if n == top {
			break
		}
		if e.Charged == 0 && e.Outcomes[OutcomeGraphQL] == 0 {
			continue
		}
		if n == 0 {
			b.WriteString("; top:")
		}
		fmt.Fprintf(&b, " %s %d", e.Endpoint, e.Charged+e.Outcomes[OutcomeGraphQL])
		if f := e.Outcomes[OutcomeFree]; f > 0 {
			fmt.Fprintf(&b, " (+%d free)", f)
		}
		b.WriteString(",")
		n++
	}
	return strings.TrimSuffix(b.String(), ",")
}

// countingTransport counts each request it sends (processUsage): under
// the cache, it sees what GitHub answered.
type countingTransport struct {
	next  http.RoundTripper
	stats *usageStats
}

func (t countingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.next.RoundTrip(req)
	stats := t.stats
	if stats == nil {
		stats = processUsage
	}
	endpoint := Endpoint(req.Method, req.URL.Path, req.URL.RawQuery)
	if err != nil {
		stats.record(endpoint, OutcomeError, 0, nil, time.Now())
		return resp, err
	}
	stats.record(endpoint, outcome(req, resp.StatusCode), resp.StatusCode, resp.Header, time.Now())
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
