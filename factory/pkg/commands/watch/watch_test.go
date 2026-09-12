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
// "no issue has a linked PR".
func TestCheckRepoHasPRsSignal(t *testing.T) {
	w := &Watcher{state: &watchState{referencedIssues: make(map[int]bool)}}

	hasPRs := len(w.state.openPRs) > 0 || !w.state.lastPRScan.IsZero()
	if hasPRs {
		t.Error("hasPRs = true for a watcher that has never scanned; want false")
	}
	if w.canQueueIssueTasks(hasPRs) {
		t.Error("canQueueIssueTasks() = true before any successful PR scan; want false")
	}
}
