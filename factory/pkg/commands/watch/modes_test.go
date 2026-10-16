package watch

import "testing"

// Each loop's mode reaches its configuration: "dryrun" makes that loop
// report only, the watcher's --dryrun makes them all.
func TestLoopModesReachTheirConfig(t *testing.T) {
	w := &Watcher{Flags: Flags{
		IssueMode: "dryrun", PRMode: "enabled", ChoresMode: "dryrun",
	}}
	if !w.issueScannerConfig().DryRun {
		t.Error("issue mode dryrun did not make the issue scanner a dry run")
	}
	if w.prScannerConfig().DryRun {
		t.Error("PR mode enabled made the PR scanner a dry run")
	}
	if !w.choresConfig().DryRun {
		t.Error("chores mode dryrun did not make the chores scheduler a dry run")
	}
	w.DryRun = true
	if !w.prScannerConfig().DryRun {
		t.Error("--dryrun did not make the PR scanner a dry run")
	}
}
