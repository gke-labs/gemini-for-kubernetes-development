package watch

import (
	"testing"
)

func TestCanQueueIssueTasks(t *testing.T) {
	tests := []struct {
		name             string
		issueMode        string
		prCachePopulated bool
		want             bool
	}{
		{
			name:             "queues when issue mode enabled and PR cache populated",
			issueMode:        "",
			prCachePopulated: true,
			want:             true,
		},
		{
			// Regression for k8s-config-connector#9259: after a restart into a
			// rate limit window the PR cache was empty, which made every issue
			// look like it had no linked PR.
			name:             "fails closed when PR cache is not populated",
			issueMode:        "",
			prCachePopulated: false,
			want:             false,
		},
		{
			name:             "never queues when issue mode is disabled",
			issueMode:        "disabled",
			prCachePopulated: true,
			want:             false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := &Watcher{Flags: Flags{IssueMode: tc.issueMode}}
			if got := w.canQueueIssueTasks(tc.prCachePopulated); got != tc.want {
				t.Errorf("canQueueIssueTasks(%v) = %v; want %v", tc.prCachePopulated, got, tc.want)
			}
		})
	}
}

// TestCheckRepoHasPRsSignal documents the state that drives canQueueIssueTasks:
// a fresh watcher has never listed open PRs, so it must not be treated as
// "no issue has a linked PR". The signal lives in EntityStateCache, which is
// what checkRepo consults.
func TestCheckRepoHasPRsSignal(t *testing.T) {
	w := &Watcher{}
	w.initComponents()

	if w.entityCache.HasOpenPRs() {
		t.Error("HasOpenPRs() = true for a watcher that has never scanned; want false")
	}
	if w.canQueueIssueTasks(w.entityCache.HasOpenPRs()) {
		t.Error("canQueueIssueTasks() = true before any successful PR scan; want false")
	}

	// A successful scan that finds no open PRs is still authoritative: the
	// cache must report itself populated, otherwise issue scanning would stall
	// forever on a repository with no open pull requests.
	w.entityCache.UpdateOpenPRs(nil)

	if !w.entityCache.HasOpenPRs() {
		t.Error("HasOpenPRs() = false after a successful scan returning no PRs; want true")
	}
	if !w.canQueueIssueTasks(w.entityCache.HasOpenPRs()) {
		t.Error("canQueueIssueTasks() = false after a successful PR scan; want true")
	}
}
