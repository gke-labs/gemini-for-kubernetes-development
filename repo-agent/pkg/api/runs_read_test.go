package api

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/go-github/v39/github"
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

// The branches runs live on are a string agreed with factory's lib.sh and
// linked by nothing. Reading a name the write path does not use finds
// no runs at all and empties the Runs tab, with every run intact on a
// branch the page never asks about.
//
// Notes are deliberately somewhere else. Runs carry teardown scripts
// for live infrastructure and are deleted by the remove path; notes are
// prose, archival, and written by a session that may be approving its
// own tool calls. One branch would point the pruning at the archive and
// put an auto-approving writer next to deployment state.
func TestRunsAndNotesAreSeparateBranches(t *testing.T) {
	if runBranchPrefix != "runs/" || runRootBranch != "runs/root" {
		t.Errorf("run branches = %q, root %q; factory's lib.sh writes runs/<run> from runs/root", runBranchPrefix, runRootBranch)
	}
	if strings.HasPrefix(notesBranch, runBranchPrefix) {
		t.Errorf("notes branch %q is among the run branches, which removing a run deletes", notesBranch)
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

// The runs with a branch of their own are one listing of the fork's
// runs/* refs, each read at its root; runs/root, the empty commit every
// run branch starts from, is not a run, and nor is anything deeper.
func TestScanRunBranches(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/m/r/git/matching-refs/heads/runs/", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `[{"ref":"refs/heads/runs/root"},{"ref":"refs/heads/runs/gke-std"},{"ref":"refs/heads/runs/a/b"}]`)
	})
	mux.HandleFunc("/repos/m/r/contents/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("ref") != "runs/gke-std" {
			http.NotFound(w, r)
			return
		}
		switch strings.TrimPrefix(r.URL.Path, "/repos/m/r/contents/") {
		case "":
			fmt.Fprint(w, `[{"type":"file","name":"runbook.md","path":"runbook.md"},{"type":"file","name":"receipt-20261005-0000.md","path":"receipt-20261005-0000.md"}]`)
		case "receipt-20261005-0000.md":
			fmt.Fprintf(w, `{"type":"file","encoding":"base64","content":%q}`, base64.StdEncoding.EncodeToString([]byte("VERIFIED (2 resources)\n")))
		default:
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	gh := github.NewClient(srv.Client())
	gh.BaseURL, _ = url.Parse(srv.URL + "/")

	runs := (&Server{}).scanRunBranches(context.Background(), gh, "m", "r")
	if len(runs) != 1 {
		t.Fatalf("runs = %v, want gke-std alone", runs)
	}
	row := runs[0]
	if row["name"] != "gke-std" || row["branch"] != "runs/gke-std" || row["htmlURL"] != "https://github.com/m/r/tree/runs/gke-std" {
		t.Errorf("row = %v", row)
	}
	if rb, _ := row["runbook"].(gin.H); rb == nil || rb["path"] != "runbook.md" {
		t.Errorf("runbook = %v, want the branch root's", row["runbook"])
	}
	if row["deployed"] != true {
		t.Errorf("deployed = %v, want true from the VERIFIED receipt", row["deployed"])
	}
}
