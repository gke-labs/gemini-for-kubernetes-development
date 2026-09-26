package api

import (
	"strings"
	"testing"
)

// Deployed-ness decides whether teardown is offered, so the rule has
// to survive a re-plan of a live run. PLANNED must not settle it:
// planning creates nothing, and letting it win would hide a running
// cluster behind a row that looks like it was never deployed.
func TestVerdictsThatSettleDeployedness(t *testing.T) {
	cases := []struct {
		verdict  string
		decisive bool
		deployed bool
	}{
		{"VERIFIED", true, true},
		{"DEPLOYED-UNVERIFIED", true, true},
		// A failed deploy may have left partial resources. Offering a
		// teardown that finds nothing is cheap; hiding one is not.
		{"FAILED", true, true},
		{"PARTIAL", true, true},
		{"TORN-DOWN", true, false},
		// Neither of these changes what exists in the cloud.
		{"PLANNED", false, false},
		{"BLOCKED", false, false},
		{"DRAFTED", false, false},
	}
	for _, tc := range cases {
		deployed, decisive := deployedVerdicts[verdictWord(tc.verdict)]
		if decisive != tc.decisive {
			t.Errorf("%s: decisive = %v, want %v", tc.verdict, decisive, tc.decisive)
			continue
		}
		if decisive && deployed != tc.deployed {
			t.Errorf("%s: deployed = %v, want %v", tc.verdict, deployed, tc.deployed)
		}
	}
}

// Receipts carry prose after the verdict word, and a run whose
// teardown says "TORN-DOWN (2 stragglers)" must still read as gone.
func TestVerdictWordIgnoresTrailingProse(t *testing.T) {
	cases := map[string]string{
		"VERIFIED":                  "VERIFIED",
		"VERIFIED (3 resources)":    "VERIFIED",
		"  torn-down  ":             "TORN-DOWN",
		"DEPLOYED-UNVERIFIED — see": "DEPLOYED-UNVERIFIED",
		"":                          "",
	}
	for in, want := range cases {
		if got := verdictWord(in); got != want {
			t.Errorf("verdictWord(%q) = %q, want %q", in, got, want)
		}
	}
}

// Every layout a run may still be sitting in is read, until the last
// one is adopted or torn down; teardown has to stay reachable.
func TestEveryRunLayoutIsRead(t *testing.T) {
	if runsPath != "docs-exploration/agent-runs" {
		t.Errorf("runsPath = %q", runsPath)
	}
	// "runs/" is a bare .gitignore entry in a great many repos and a
	// bare pattern matches at any depth, so this path was ignored
	// wherever it appeared — silently.
	if runsPath == "docs-exploration/runs" {
		t.Error("runsPath is back under runs/, which repositories commonly gitignore")
	}
	want := map[string]bool{
		"docs-exploration/runbook-deployments": true,
		"docs-exploration/runs":                true,
	}
	for _, p := range legacyRunPaths {
		if !want[p] {
			t.Errorf("unexpected legacy path %q", p)
		}
		delete(want, p)
	}
	for p := range want {
		t.Errorf("legacy path %q is no longer read; runs there become invisible", p)
	}
}

// The intent rides the claim so it dies when the claim is consumed. A
// board-level field is shared by every run and outlives all of them,
// which is how a brief typed for one deployment came to steer the
// next — the fossilized explore-guidance seen live on several boards.
func TestClampIntentKeepsAClaimWritable(t *testing.T) {
	// "|" is the claim's own separator; an intent carrying one would
	// split into a bogus field on the way back.
	if got := clampIntent("five nodes | us-east1"); got != "five nodes / us-east1" {
		t.Errorf("clampIntent did not neutralise the separator: %q", got)
	}
	// Newlines have to go: the claim is one annotation value.
	if got := clampIntent("five nodes\n\nand us-east1"); got != "five nodes and us-east1" {
		t.Errorf("clampIntent left newlines: %q", got)
	}
	// All annotations on an object share a 256KB budget.
	long := clampIntent(strings.Repeat("x", intentClaimLimit*2))
	if len(long) != intentClaimLimit {
		t.Errorf("clampIntent len = %d, want %d", len(long), intentClaimLimit)
	}
}

func TestFirstNonEmptyPrefersTheNewSpelling(t *testing.T) {
	if got := firstNonEmpty("", "  ", "instance-name"); got != "instance-name" {
		t.Errorf("firstNonEmpty = %q", got)
	}
	if got := firstNonEmpty("name", "instance"); got != "name" {
		t.Errorf("firstNonEmpty = %q, want the first", got)
	}
	if got := firstNonEmpty("", " "); got != "" {
		t.Errorf("firstNonEmpty = %q, want empty", got)
	}
}
