package watch

import (
	"testing"
)

// TestIssuesEnabled pins the modes the issue scanner goroutine starts in.
//
// Mode gating decides whether the subcontroller runs at all, rather than being
// re-evaluated inside a shared cycle: a mode that does not scan issues should
// not pay for a scanner that wakes up every interval to find nothing to do.
func TestIssuesEnabled(t *testing.T) {
	for _, tc := range []struct {
		mode      string
		issueMode string
		want      bool
	}{
		{mode: "all", want: true},
		{mode: "scan", want: true},
		{mode: "scan-issue", want: true},
		{mode: "scan-pr", want: false},
		{mode: "run", want: false},
		{mode: "all", issueMode: "disabled", want: false},
	} {
		w := &Watcher{Flags: Flags{Mode: tc.mode, IssueMode: tc.issueMode}}
		if got := w.issuesEnabled(); got != tc.want {
			t.Errorf("issuesEnabled(mode=%q, issueMode=%q) = %v, want %v", tc.mode, tc.issueMode, got, tc.want)
		}
	}
}

// TestEntityCacheStartsUnpopulated documents the signal the issue scanner fails
// closed on: a fresh watcher has never listed open PRs, and that must not be
// read as "no issue has a linked PR".
func TestEntityCacheStartsUnpopulated(t *testing.T) {
	w := &Watcher{}
	w.initComponents()

	if w.entityCache.HasOpenPRs() {
		t.Error("HasOpenPRs() = true for a watcher that has never scanned; want false")
	}

	// A successful scan that finds no open PRs is still authoritative: the
	// cache must report itself populated, otherwise issue scanning would stall
	// forever on a repository with no open pull requests.
	w.entityCache.UpdateOpenPRs(nil)

	if !w.entityCache.HasOpenPRs() {
		t.Error("HasOpenPRs() = false after a successful scan returning no PRs; want true")
	}
}

// TestPRsEnabled pins the modes the pull request scanner goroutine starts in.
//
// The disabled case used to be checked at the top of the scan itself; it is
// asserted here because the gate moved to where the goroutine is started.
func TestPRsEnabled(t *testing.T) {
	for _, tc := range []struct {
		mode   string
		prMode string
		want   bool
	}{
		{mode: "all", want: true},
		{mode: "scan", want: true},
		{mode: "scan-pr", want: true},
		{mode: "scan-issue", want: false},
		{mode: "run", want: false},
		{mode: "all", prMode: "disabled", want: false},
	} {
		w := &Watcher{Flags: Flags{Mode: tc.mode, PRMode: tc.prMode}}
		if got := w.prsEnabled(); got != tc.want {
			t.Errorf("prsEnabled(mode=%q, prMode=%q) = %v, want %v", tc.mode, tc.prMode, got, tc.want)
		}
	}
}

// TestChoresEnabled pins the modes the chore scheduler goroutine starts in.
// These are inherited from when chore scanning lived inside the slow PR cycle,
// "scan-pr" included.
func TestChoresEnabled(t *testing.T) {
	for _, tc := range []struct {
		mode       string
		choresMode string
		want       bool
	}{
		{mode: "all", want: true},
		{mode: "scan", want: true},
		{mode: "scan-pr", want: true},
		{mode: "scan-issue", want: false},
		{mode: "run", want: false},
		{mode: "all", choresMode: "disabled", want: false},
	} {
		w := &Watcher{Flags: Flags{Mode: tc.mode, ChoresMode: tc.choresMode}}
		if got := w.choresEnabled(); got != tc.want {
			t.Errorf("choresEnabled(mode=%q, choresMode=%q) = %v, want %v", tc.mode, tc.choresMode, got, tc.want)
		}
	}
}
