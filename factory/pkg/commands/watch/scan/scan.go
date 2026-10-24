// Package scan keeps the one set of GitHub listings the issue and pull request
// scanners share (factory/design/watch-rules.md, "One scan, events from
// diffs"). Without it each scanner lists for itself and the same queries run
// twice: both list every bot's assigned items, and both list the trigger label.
//
// A Store holds a snapshot of the items the watcher tracks: every open issue
// or pull request that carries the trigger label, is assigned to a bot in the
// pool, or was opened by the watcher's login. It is brought up to date
// incrementally, by one listing of everything updated since the last pass
// (GitHub's since, less a margin), and in full every so often and whenever no
// snapshot can be trusted. The scanners read their lists out of it, in the
// shapes their own listings had, so that what they decide does not change:
// the fast passes still see the first page of each bot's assigned items,
// newest updated first; the sweeps still see every labelled or assigned item.
//
// The snapshot persists in the queue directory, so the restart the overseer
// does every few minutes continues from it instead of listing everything
// again. It holds what the scanners read of an item (number, title, body,
// state, labels, assignees, author, times), not HTTP responses.
package scan

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
)

const (
	// DefaultFullEvery is how often the snapshot is rebuilt from full
	// listings, as a safety net for whatever an incremental pass missed.
	DefaultFullEvery = 10 * time.Minute
	// DefaultMargin is how far before the last pass's start an incremental
	// listing reaches back, so an item updated while a pass ran, or a clock
	// skewed against GitHub's, is not missed. Seeing an item twice is
	// harmless.
	DefaultMargin = 2 * time.Minute
	// DefaultReuseFor is how long a refresh serves later callers: the two
	// scanners run on their own tickers, and the second of a pair of cycles
	// reads the first one's refresh instead of listing again.
	DefaultReuseFor = 30 * time.Second

	// stateVersion is the persisted snapshot's format. A different one is
	// treated as no snapshot. 2: items keep their author_association.
	stateVersion = 2
)

// Lister is the GitHub call a Store makes. *github.Client implements it.
type Lister interface {
	ListIssues(ctx context.Context, opts *githubv39.IssueListByRepoOptions) ([]*githubv39.Issue, *githubv39.Response, error)
}

var _ Lister = (*github.Client)(nil)

// labelledLister lists the open pull requests carrying any of a set of
// labels in one query (github.Client.OpenPRsLabelled): how a full refresh
// lists Config.Labels.
type labelledLister interface {
	OpenPRsLabelled(ctx context.Context, labels []string) ([]*githubv39.Issue, error)
}

var _ labelledLister = (*github.Client)(nil)

// Config says what a Store tracks and how it refreshes.
type Config struct {
	// Repo is "owner/repo", recorded with the snapshot so that a snapshot of
	// another repository is never read.
	Repo string
	// TriggerLabel, BotUsers and GitHubLogin are what makes an item tracked:
	// open, and labelled with TriggerLabel, assigned to one of BotUsers, or
	// opened by GitHubLogin.
	TriggerLabel string
	BotUsers     []string
	GitHubLogin  string
	// Labels make an open pull request carrying any of them tracked too:
	// the overseer-namespaced labels its rules require that no other
	// listing reaches (rules.Set.ListingLabels). A full refresh lists them
	// in one query; between full refreshes the incremental listing sees a
	// pull request labelled since.
	Labels []string
	// FullEvery, Margin and ReuseFor default to DefaultFullEvery,
	// DefaultMargin and DefaultReuseFor.
	FullEvery time.Duration
	Margin    time.Duration
	ReuseFor  time.Duration
	// StatePath is the file the snapshot persists in; empty keeps it in
	// memory only.
	StatePath string
}

// Stats describes the last refresh, for the log.
type Stats struct {
	Full    bool
	Calls   int
	Changed int
	Items   int
}

// Store is the shared snapshot. It is safe for concurrent use: the issue and
// pull request scanners run in their own goroutines.
type Store struct {
	cfg Config
	gh  Lister
	now func() time.Time

	mu sync.Mutex
	// loaded is whether the persisted snapshot has been read (or found
	// missing); it is read once, on the first refresh.
	loaded bool
	items  map[int]*githubv39.Issue
	// cursor is where the next incremental listing starts (GitHub's since);
	// zero means no snapshot, so the next refresh is full.
	cursor time.Time
	// lastFull is when the snapshot was last rebuilt from full listings.
	lastFull time.Time
	// refreshed is when the last refresh succeeded, for ReuseFor.
	refreshed time.Time
	// lastErr is the last refresh's error, returned to callers it serves.
	lastErr error
	// written is the persisted form last written, so an unchanged snapshot
	// is not rewritten (the queue directory is committed to git).
	written []byte
	stats   Stats
}

// New constructs a Store listing through gh.
func New(cfg Config, gh Lister) *Store {
	if cfg.FullEvery <= 0 {
		cfg.FullEvery = DefaultFullEvery
	}
	if cfg.Margin <= 0 {
		cfg.Margin = DefaultMargin
	}
	if cfg.ReuseFor <= 0 {
		cfg.ReuseFor = DefaultReuseFor
	}
	return &Store{cfg: cfg, gh: gh, now: time.Now, items: map[int]*githubv39.Issue{}}
}

// Refresh brings the snapshot up to date, unless a refresh within ReuseFor
// already did. It lists in full when no snapshot can be trusted or a full
// rebuild is due, and otherwise lists what changed since the last pass. On
// error the snapshot is left as it was, so readers get the last good view,
// and the error says the view may be stale.
func (s *Store) Refresh(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.loaded {
		s.load()
		s.loaded = true
	}
	start := s.now()
	if !s.refreshed.IsZero() && start.Sub(s.refreshed) < s.cfg.ReuseFor {
		return s.lastErr
	}

	var err error
	if s.cursor.IsZero() || start.Sub(s.lastFull) >= s.cfg.FullEvery {
		err = s.full(ctx, start)
	} else {
		err = s.incremental(ctx, start)
	}
	s.lastErr = err
	if err != nil {
		// The next call tries again rather than serving the failure for
		// ReuseFor.
		s.refreshed = time.Time{}
		return err
	}
	s.refreshed = start
	s.persist()
	klog.Infof("Shared scan: %s refresh, %d listing calls, %d items changed, %d tracked", map[bool]string{true: "full", false: "incremental"}[s.stats.Full], s.stats.Calls, s.stats.Changed, s.stats.Items)
	return nil
}

// LastStats describes the last successful refresh.
func (s *Store) LastStats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats
}

// full rebuilds the snapshot from the listings whose union is the tracked
// set: the trigger label, each bot's assignments, the watcher's own issues.
// A failed listing leaves the previous snapshot whole: a partial rebuild
// would drop items as though they had closed.
func (s *Store) full(ctx context.Context, start time.Time) error {
	calls := 0
	items := map[int]*githubv39.Issue{}
	queries := []*githubv39.IssueListByRepoOptions{}
	if s.cfg.TriggerLabel != "" {
		queries = append(queries, &githubv39.IssueListByRepoOptions{Labels: []string{s.cfg.TriggerLabel}, State: "open"})
	}
	for _, bot := range s.cfg.BotUsers {
		queries = append(queries, &githubv39.IssueListByRepoOptions{Assignee: bot, State: "open"})
	}
	if s.cfg.GitHubLogin != "" {
		queries = append(queries, &githubv39.IssueListByRepoOptions{Creator: s.cfg.GitHubLogin, State: "open"})
	}
	for _, q := range queries {
		got, n, err := s.listAll(ctx, q)
		calls += n
		if err != nil {
			return fmt.Errorf("full listing: %w", err)
		}
		for _, item := range got {
			if s.tracked(item) {
				items[item.GetNumber()] = reduce(item)
			}
		}
	}
	if ll, ok := s.gh.(labelledLister); ok && len(s.cfg.Labels) > 0 {
		got, err := ll.OpenPRsLabelled(ctx, s.cfg.Labels)
		calls++
		if err != nil {
			return fmt.Errorf("full listing of %v: %w", s.cfg.Labels, err)
		}
		for _, item := range got {
			if s.tracked(item) {
				items[item.GetNumber()] = reduce(item)
			}
		}
	}
	s.stats = Stats{Full: true, Calls: calls, Changed: changedCount(s.items, items), Items: len(items)}
	s.items = items
	s.lastFull = start
	s.cursor = start.Add(-s.cfg.Margin)
	return nil
}

// incremental lists every item updated since the cursor, open or closed,
// tracked or not, and updates the snapshot with each: an item that now
// matches is put in, one that no longer does (closed, unlabelled,
// unassigned) is taken out. That is one listing for the whole repository,
// usually a page or none, where the scanners' own listings were a page per
// bot and per label.
func (s *Store) incremental(ctx context.Context, start time.Time) error {
	got, calls, err := s.listAll(ctx, &githubv39.IssueListByRepoOptions{
		State:     "all",
		Sort:      "updated",
		Direction: "desc",
		Since:     s.cursor,
	})
	if err != nil {
		return fmt.Errorf("incremental listing: %w", err)
	}
	changed := 0
	for _, item := range got {
		num := item.GetNumber()
		old, had := s.items[num]
		if s.tracked(item) {
			r := reduce(item)
			if !had || !sameItem(old, r) {
				changed++
			}
			s.items[num] = r
		} else if had {
			delete(s.items, num)
			changed++
		}
	}
	s.stats = Stats{Calls: calls, Changed: changed, Items: len(s.items)}
	s.cursor = start.Add(-s.cfg.Margin)
	return nil
}

// listAll follows a query's pages, returning the items and the number of
// requests made.
func (s *Store) listAll(ctx context.Context, opts *githubv39.IssueListByRepoOptions) ([]*githubv39.Issue, int, error) {
	opts.PerPage = 100
	var all []*githubv39.Issue
	calls := 0
	for {
		page, resp, err := s.gh.ListIssues(ctx, opts)
		calls++
		if err != nil {
			return nil, calls, err
		}
		all = append(all, page...)
		if resp == nil || resp.NextPage == 0 {
			return all, calls, nil
		}
		opts.Page = resp.NextPage
	}
}

// tracked reports whether an item is the watcher's to know about.
func (s *Store) tracked(item *githubv39.Issue) bool {
	if item.GetState() != "" && item.GetState() != "open" {
		return false
	}
	if s.cfg.TriggerLabel != "" && hasLabel(item, s.cfg.TriggerLabel) {
		return true
	}
	if item.IsPullRequest() && s.ruleLabelled(item) {
		return true
	}
	if s.cfg.GitHubLogin != "" && strings.EqualFold(item.GetUser().GetLogin(), s.cfg.GitHubLogin) {
		return true
	}
	for _, bot := range s.cfg.BotUsers {
		if assignedTo(item, bot) {
			return true
		}
	}
	return false
}

// Labelled is every tracked item carrying the trigger label, issues and pull
// requests alike, as the full label listing returned them.
func (s *Store) Labelled() []*githubv39.Issue {
	return s.view(func(item *githubv39.Issue) bool { return hasLabel(item, s.cfg.TriggerLabel) }, 0)
}

// RuleLabelled is every tracked pull request carrying one of
// Config.Labels.
func (s *Store) RuleLabelled() []*githubv39.Issue {
	return s.view(func(item *githubv39.Issue) bool { return item.IsPullRequest() && s.ruleLabelled(item) }, 0)
}

// ruleLabelled is whether item carries one of Config.Labels.
func (s *Store) ruleLabelled(item *githubv39.Issue) bool {
	return slices.ContainsFunc(s.cfg.Labels, func(l string) bool { return hasLabel(item, l) })
}

// Assigned is every tracked item assigned to user, as the full assignee
// listing returned them.
func (s *Store) Assigned(user string) []*githubv39.Issue {
	return s.view(func(item *githubv39.Issue) bool { return assignedTo(item, user) }, 0)
}

// AssignedFirst is the first n items assigned to user, newest updated first:
// the first page of the assignee listing the fast passes make.
func (s *Store) AssignedFirst(user string, n int) []*githubv39.Issue {
	return s.view(func(item *githubv39.Issue) bool { return assignedTo(item, user) }, n)
}

// CreatedFirst is the first n items opened by login, newest updated first: the
// first page of the creator listing the issue scanner's fast pass makes.
func (s *Store) CreatedFirst(login string, n int) []*githubv39.Issue {
	return s.view(func(item *githubv39.Issue) bool { return strings.EqualFold(item.GetUser().GetLogin(), login) }, n)
}

// view is the tracked items matching keep, newest updated first (ties by
// number, newest first), at most n of them when n > 0. Each is a copy: a
// scanner that records a label it just added on its item does not write into
// the snapshot another goroutine reads.
func (s *Store) view(keep func(*githubv39.Issue) bool, n int) []*githubv39.Issue {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*githubv39.Issue
	for _, item := range s.items {
		if keep(item) {
			out = append(out, item)
		}
	}
	slices.SortFunc(out, func(a, b *githubv39.Issue) int {
		if c := b.GetUpdatedAt().Compare(a.GetUpdatedAt()); c != 0 {
			return c
		}
		return cmp.Compare(b.GetNumber(), a.GetNumber())
	})
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	copies := make([]*githubv39.Issue, len(out))
	for i, item := range out {
		copies[i] = clone(item)
	}
	return copies
}

// persisted is the snapshot's form on disk.
type persisted struct {
	Version      int                `json:"version"`
	Repo         string             `json:"repo"`
	TriggerLabel string             `json:"triggerLabel"`
	BotUsers     []string           `json:"botUsers"`
	GitHubLogin  string             `json:"githubLogin"`
	Labels       []string           `json:"labels,omitempty"`
	Cursor       time.Time          `json:"cursor"`
	LastFull     time.Time          `json:"lastFull"`
	Items        []*githubv39.Issue `json:"items"`
}

// load reads the persisted snapshot. Anything it cannot trust - no file, a
// corrupt one, another version, another repository, another definition of
// what is tracked - is no snapshot: the cursor stays zero and the next
// refresh lists in full.
func (s *Store) load() {
	if s.cfg.StatePath == "" {
		return
	}
	data, err := os.ReadFile(s.cfg.StatePath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			klog.Warningf("Shared scan: reading %s: %v; listing in full", s.cfg.StatePath, err)
		}
		return
	}
	var p persisted
	if err := json.Unmarshal(data, &p); err != nil {
		klog.Warningf("Shared scan: %s is not a snapshot (%v); listing in full", s.cfg.StatePath, err)
		return
	}
	if p.Version != stateVersion || p.Repo != s.cfg.Repo || p.TriggerLabel != s.cfg.TriggerLabel ||
		p.GitHubLogin != s.cfg.GitHubLogin || !slices.Equal(p.BotUsers, s.sortedBots()) || !slices.Equal(p.Labels, s.cfg.Labels) {
		klog.Infof("Shared scan: %s is a snapshot of something else; listing in full", s.cfg.StatePath)
		return
	}
	items := make(map[int]*githubv39.Issue, len(p.Items))
	for _, item := range p.Items {
		if item.GetNumber() > 0 {
			items[item.GetNumber()] = item
		}
	}
	s.items, s.cursor, s.lastFull = items, p.Cursor, p.LastFull
	s.written = data
}

// persist writes the snapshot when it changed, atomically: a temporary file
// renamed into place, so a crash mid-write leaves the old snapshot or the new
// one, never half of either. A failed write is logged; the snapshot in memory
// is still good, and the next restart lists in full.
func (s *Store) persist() {
	if s.cfg.StatePath == "" {
		return
	}
	p := persisted{
		Version: stateVersion, Repo: s.cfg.Repo, TriggerLabel: s.cfg.TriggerLabel,
		BotUsers: s.sortedBots(), GitHubLogin: s.cfg.GitHubLogin, Labels: s.cfg.Labels,
		Cursor: s.cursor, LastFull: s.lastFull,
	}
	for _, item := range s.items {
		p.Items = append(p.Items, item)
	}
	slices.SortFunc(p.Items, func(a, b *githubv39.Issue) int { return cmp.Compare(a.GetNumber(), b.GetNumber()) })
	data, err := json.MarshalIndent(p, "", " ")
	if err != nil {
		klog.Warningf("Shared scan: encoding the snapshot: %v", err)
		return
	}
	if slices.Equal(data, s.written) {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.cfg.StatePath), 0o755); err != nil {
		klog.Warningf("Shared scan: %v", err)
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.cfg.StatePath), ".scan-state-*")
	if err != nil {
		klog.Warningf("Shared scan: %v", err)
		return
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(tmp.Name())
		klog.Warningf("Shared scan: writing the snapshot: %v", errors.Join(werr, cerr))
		return
	}
	if err := os.Rename(tmp.Name(), s.cfg.StatePath); err != nil {
		_ = os.Remove(tmp.Name())
		klog.Warningf("Shared scan: %v", err)
		return
	}
	s.written = data
}

func (s *Store) sortedBots() []string {
	bots := slices.Clone(s.cfg.BotUsers)
	slices.Sort(bots)
	return bots
}

// reduce keeps what the scanners read of an item.
func reduce(item *githubv39.Issue) *githubv39.Issue {
	r := &githubv39.Issue{
		Number:            item.Number,
		Title:             item.Title,
		Body:              item.Body,
		State:             item.State,
		HTMLURL:           item.HTMLURL,
		CreatedAt:         item.CreatedAt,
		UpdatedAt:         item.UpdatedAt,
		ClosedAt:          item.ClosedAt,
		PullRequestLinks:  item.PullRequestLinks,
		AuthorAssociation: item.AuthorAssociation,
	}
	if u := item.User; u != nil {
		r.User = &githubv39.User{Login: u.Login, Type: u.Type}
	}
	for _, l := range item.Labels {
		r.Labels = append(r.Labels, &githubv39.Label{Name: l.Name})
	}
	for _, a := range item.Assignees {
		r.Assignees = append(r.Assignees, &githubv39.User{Login: a.Login, Type: a.Type})
	}
	if a := item.Assignee; a != nil {
		r.Assignee = &githubv39.User{Login: a.Login, Type: a.Type}
	}
	return r
}

// clone copies an item deep enough that changing its labels or assignees
// does not change the snapshot's.
func clone(item *githubv39.Issue) *githubv39.Issue {
	c := *item
	c.Labels = make([]*githubv39.Label, len(item.Labels))
	for i, l := range item.Labels {
		lc := *l
		c.Labels[i] = &lc
	}
	c.Assignees = make([]*githubv39.User, len(item.Assignees))
	for i, a := range item.Assignees {
		ac := *a
		c.Assignees[i] = &ac
	}
	if item.Labels == nil {
		c.Labels = nil
	}
	if item.Assignees == nil {
		c.Assignees = nil
	}
	return &c
}

func hasLabel(item *githubv39.Issue, label string) bool {
	for _, l := range item.Labels {
		if strings.EqualFold(l.GetName(), label) {
			return true
		}
	}
	return false
}

func assignedTo(item *githubv39.Issue, user string) bool {
	for _, a := range item.Assignees {
		if strings.EqualFold(a.GetLogin(), user) {
			return true
		}
	}
	return false
}

func sameItem(a, b *githubv39.Issue) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return slices.Equal(ja, jb)
}

func changedCount(old, current map[int]*githubv39.Issue) int {
	n := 0
	for num, item := range current {
		if o, ok := old[num]; !ok || !sameItem(o, item) {
			n++
		}
	}
	for num := range old {
		if _, ok := current[num]; !ok {
			n++
		}
	}
	return n
}
