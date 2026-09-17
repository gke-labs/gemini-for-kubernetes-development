package concurrency

import (
	"sync"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/common"
	githubv39 "github.com/google/go-github/v39/github"
)

// EntityStateCache is a thread-safe cache of the open pull requests and issues
// the scanners have observed, shared across subcontroller loops.
//
// Its purpose is to let a consumer answer "is this entity still open?" without
// a GitHub request. A miss is always safe: callers are expected to fall back to
// GitHub, which stays authoritative. What is not safe is treating an
// unpopulated cache as "nothing is open", so each half of the cache records
// whether a scan has published to it yet.
//
// Note that the per-entity processed timestamps and commit SHAs deliberately do
// *not* live here. Each scanner keeps its own: nothing outside the pull request
// scanner reads which commit a pull request was last reviewed at, and nothing
// outside the issue scanner reads when an issue was last worked on. What this
// cache is for is the state that genuinely crosses subcontrollers - the open
// pull requests, which the issue scanner reads to tell that an issue already
// has a fix in flight and the sandbox reconciler reads to tell that a sandbox
// is still wanted.
type EntityStateCache struct {
	mu               sync.RWMutex
	openPRs          []*githubv39.PullRequest
	referencedIssues map[int]bool
	openIssues       map[int]bool
	lastPRScan       time.Time
	lastIssueScan    time.Time
}

// NewEntityStateCache creates a new EntityStateCache initialized with empty maps.
func NewEntityStateCache() *EntityStateCache {
	return &EntityStateCache{
		referencedIssues: make(map[int]bool),
		openIssues:       make(map[int]bool),
	}
}

// UpdateOpenPRs replaces the cached open PR list and recomputes the referenced issues map.
func (c *EntityStateCache) UpdateOpenPRs(prs []*githubv39.PullRequest) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if prs == nil {
		c.openPRs = nil
	} else {
		c.openPRs = make([]*githubv39.PullRequest, len(prs))
		copy(c.openPRs, prs)
	}

	c.referencedIssues = make(map[int]bool)
	for _, pr := range prs {
		if pr == nil {
			continue
		}
		for num := range common.GetClosingIssues(pr) {
			c.referencedIssues[num] = true
		}
	}
	c.lastPRScan = time.Now()
}

// IsOpenPR reports whether the pull request with the given number is currently open.
func (c *EntityStateCache) IsOpenPR(prNum int) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	for _, pr := range c.openPRs {
		if pr != nil && pr.GetNumber() == prNum {
			return true
		}
	}
	return false
}

// HasOpenPRs reports whether a pull request scan has published to the cache.
//
// It is true even when the scan found no open pull requests: "scanned, found
// nothing" and "never scanned" are different answers, and only the latter
// means IsOpenPR cannot be trusted.
func (c *EntityStateCache) HasOpenPRs() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return len(c.openPRs) > 0 || !c.lastPRScan.IsZero()
}

// HasOpenIssues reports whether an issue scan has published to the cache,
// with the same "scanned, found nothing" semantics as HasOpenPRs.
func (c *EntityStateCache) HasOpenIssues() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return len(c.openIssues) > 0 || !c.lastIssueScan.IsZero()
}

// IsIssueReferenced returns true if the issue number is referenced by any open PR.
func (c *EntityStateCache) IsIssueReferenced(issueNum int) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.referencedIssues[issueNum]
}

// GetReferencedIssuesMap returns a copy of the referenced issues map.
func (c *EntityStateCache) GetReferencedIssuesMap() map[int]bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	res := make(map[int]bool, len(c.referencedIssues))
	for k, v := range c.referencedIssues {
		res[k] = v
	}
	return res
}

// SetOpenIssueNumbers replaces the set of issues known to be open and marks the
// issue half of the cache as populated.
func (c *EntityStateCache) SetOpenIssueNumbers(nums []int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.openIssues = make(map[int]bool, len(nums))
	for _, num := range nums {
		c.openIssues[num] = true
	}
	c.lastIssueScan = time.Now()
}

// IsOpenIssue reports whether an issue number is recorded as currently open.
func (c *EntityStateCache) IsOpenIssue(num int) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.openIssues[num]
}
