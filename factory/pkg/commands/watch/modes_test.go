package watch

import "testing"

// Each loop's mode reaches its configuration: "dryrun" makes that loop
// report only, the watcher's --dryrun makes them all; the review mode and
// the chores' include/exclude lists are passed through.
func TestLoopModesReachTheirConfig(t *testing.T) {
	w := &Watcher{Flags: Flags{
		IssueMode: "dryrun", PRMode: "enabled", ChoresMode: "dryrun", ReviewMode: "disabled",
		ChoresInclude: []string{"triage"}, ChoresExclude: []string{"deps-*"},
	}}
	if !w.issueScannerConfig().DryRun {
		t.Error("issue mode dryrun did not make the issue scanner a dry run")
	}
	if pc := w.prScannerConfig(); pc.DryRun || pc.ReviewMode != "disabled" {
		t.Errorf("PR scanner config = dry run %v, review mode %q; want false, disabled", pc.DryRun, pc.ReviewMode)
	}
	cc := w.choresConfig()
	if !cc.DryRun || len(cc.Include) != 1 || len(cc.Exclude) != 1 {
		t.Errorf("chores config = %+v; want a dry run with the include and exclude lists", cc)
	}
	w.DryRun = true
	if !w.prScannerConfig().DryRun {
		t.Error("--dryrun did not make the PR scanner a dry run")
	}
}
