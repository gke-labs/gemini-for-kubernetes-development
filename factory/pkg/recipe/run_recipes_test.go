package recipe

import (
	"strconv"
	"strings"
	"testing"
)

// The run recipes' asks: run-plan, run-deploy and run-teardown carry the
// prompts the run_*.txt templates did, and these are those templates'
// tests, rendering the ask a run's phase sends.

// runParams are what a test sets of a run; renderRun makes them inputs.
type runParams struct {
	RepoName, HTMLURL, Name, Intent, Runbook string
	Target                                   int
}

// renderRun renders run-<mode>'s ask as a run with p gets it.
func renderRun(t *testing.T, mode string, p runParams) string {
	t.Helper()
	_, rec, err := Builtin(RunRecipe(mode))
	if err != nil {
		t.Fatalf("Builtin(%s): %v", RunRecipe(mode), err)
	}
	std := map[string]string{"repo_owner": "o", "repo_name": p.RepoName, "repo_url": p.HTMLURL, "url": p.HTMLURL, "disclose": "true"}
	overrides := map[string]string{"run": p.Name, "resource_prefix": "r-" + p.Name}
	if p.Intent != "" {
		overrides["intent"] = p.Intent
	}
	if p.Runbook != "" {
		overrides["runbook"] = p.Runbook
	}
	if p.Target > 0 {
		overrides["target_pr"] = strconv.Itoa(p.Target)
	}
	inputs, err := rec.ResolveInputs(std, overrides)
	if err != nil {
		t.Fatal(err)
	}
	for i, s := range rec.Start.Steps {
		if s.Ask != "" {
			out, err := render(s.Label(i), s.Ask, templateData{Inputs: inputs, Steps: map[string]*StepResult{}}, "")
			if err != nil {
				t.Fatal(err)
			}
			return out
		}
	}
	t.Fatalf("%s has no ask", rec.Name)
	return ""
}

func TestRunPromptsCarryTheRunDirectory(t *testing.T) {
	for _, mode := range []string{"plan", "deploy", "teardown"} {
		got := renderRun(t, mode, runParams{RepoName: "open-rl", Name: "deploy-gke-k8s1"})
		// The run's worktree, where run-prepare puts its branch
		// (lib.sh's RUN_DIR), and the branch it is.
		for _, want := range []string{".factory-run/", "runs/deploy-gke-k8s1"} {
			if !strings.Contains(got, want) {
				t.Errorf("%s prompt does not name %s", mode, want)
			}
		}
		// Runs are not directories of a shared branch any more.
		for _, gone := range []string{"docs-exploration/agent-runs", "docs-exploration/runbooks/", "research/runs"} {
			if strings.Contains(got, gone) {
				t.Errorf("%s prompt still points at %s", mode, gone)
			}
		}
	}
}

func TestPlanPromptOrdersProseBeforeShell(t *testing.T) {
	got := renderRun(t, "plan", runParams{RepoName: "open-rl", Name: "deploy-gke-k8s1"})
	md, sh := strings.Index(got, "runbook.md"), strings.Index(got, "deploy.sh")
	if md < 0 || sh < 0 {
		t.Fatalf("plan prompt must name both runbook.md and deploy.sh")
	}
	if md > sh {
		t.Error("plan prompt introduces deploy.sh before runbook.md")
	}
	if !strings.Contains(got, "revise it rather than replacing it") {
		t.Error("plan prompt must tell a re-plan to revise, not regenerate — otherwise deploy-learned corrections are lost")
	}
}

func TestDeployPromptReconcilesTheProcedure(t *testing.T) {
	got := renderRun(t, "deploy", runParams{RepoName: "open-rl", Name: "deploy-gke-k8s1"})
	for _, want := range []string{"RECONCILE runbook.md", "whether the deploy succeeded or failed", "Procedure amended"} {
		if !strings.Contains(got, want) {
			t.Errorf("deploy prompt missing %q", want)
		}
	}
	// The read-only rule and its consolation prize both existed only
	// because the runbook was shared. They must not survive.
	for _, gone := range []string{"READ-ONLY TO YOU", "Recommended runbook changes"} {
		if strings.Contains(got, gone) {
			t.Errorf("deploy prompt still carries %q, which only made sense for a shared runbook", gone)
		}
	}
}

func TestIntentIsOptional(t *testing.T) {
	with := renderRun(t, "plan", runParams{RepoName: "r", Name: "n", Intent: "3 ubuntu nodes"})
	if !strings.Contains(with, "3 ubuntu nodes") {
		t.Error("intent not rendered")
	}
	without := renderRun(t, "plan", runParams{RepoName: "r", Name: "n"})
	if strings.Contains(without, "What the owner asked for") {
		t.Error("empty intent still rendered its heading")
	}
}

func TestUnknownModeIsRejected(t *testing.T) {
	if _, _, err := Builtin(RunRecipe("execute")); err == nil {
		t.Error("expected an error for a mode that does not exist")
	}
}

func TestPlanPromptDistinguishesAmendmentFromBrief(t *testing.T) {
	got := renderRun(t, "plan", runParams{RepoName: "r", Name: "n", Intent: "5 nodes"})
	for _, want := range []string{"AMENDMENT to it", "this is the whole brief"} {
		if !strings.Contains(got, want) {
			t.Errorf("plan prompt missing %q — a refinement could be read as a replacement brief", want)
		}
	}
}

func TestPlanReceiptCarriesRunningResourcesForward(t *testing.T) {
	got := renderRun(t, "plan", runParams{RepoName: "r", Name: "n"})
	for _, want := range []string{"Currently deployed", "left\n   RUNNING", "has not been applied"} {
		if !strings.Contains(got, want) {
			t.Errorf("plan prompt missing %q", want)
		}
	}
}

func TestPlanPromptVerifiesFeasibility(t *testing.T) {
	got := renderRun(t, "plan", runParams{RepoName: "open-rl", Name: "deploy-gke-k8s1"})
	for _, want := range []string{
		"VERIFY FEASIBILITY",
		"testIamPermissions",
		"✗ MISSING",
		"ACCOUNT-AGNOSTIC",
		"docs-exploration/SKILL.md",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("plan prompt missing %q", want)
		}
	}
}

func TestPlanDegradesWhenParametersCannotResolve(t *testing.T) {
	got := renderRun(t, "plan", runParams{RepoName: "open-rl", Name: "deploy-gke-k8s1"})
	for _, want := range []string{
		"IF THE PARAMETERS CANNOT BE RESOLVED",
		"BLOCKED instead of PLANNED",
		"Needs from owner",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("plan prompt missing %q", want)
		}
	}
}

func TestThereIsNoDraftMode(t *testing.T) {
	if _, _, err := Builtin(RunRecipe("draft")); err == nil {
		t.Error("draft resolved to a prompt; plan is supposed to do the drafting")
	}
}

func TestPlanPromptKnowsItStartedFromARunbook(t *testing.T) {
	with := renderRun(t, "plan", runParams{RepoName: "r", Name: "pr-42", Runbook: "gke"})
	for _, want := range []string{`started from the runbook "gke"`, "RESOURCE_PREFIX is this run's", "Change nothing else.", "verify it here"} {
		if !strings.Contains(with, want) {
			t.Errorf("plan prompt missing %q", want)
		}
	}
	if amended := renderRun(t, "plan", runParams{RepoName: "r", Name: "pr-42", Intent: "5 nodes", Runbook: "gke"}); !strings.Contains(amended, "Change nothing else beyond what the owner asked for above.") {
		t.Error("with an intent, the prompt does not allow the owner's changes")
	}
	if without := renderRun(t, "plan", runParams{RepoName: "r", Name: "pr-42"}); strings.Contains(without, "started from the runbook") {
		t.Error("the runbook paragraph renders for a run started from nothing")
	}
}

func TestPlanPromptKnowsItsTarget(t *testing.T) {
	with := renderRun(t, "plan", runParams{RepoName: "r", HTMLURL: "https://github.com/o/r", Name: "gke-pr42", Target: 42})
	for _, want := range []string{"deploys pull request #42 (https://github.com/o/r/pull/42)", "target.env", "do not edit it", "TARGET_SHA"} {
		if !strings.Contains(with, want) {
			t.Errorf("plan prompt missing %q", want)
		}
	}
	if without := renderRun(t, "plan", runParams{RepoName: "r", Name: "gke"}); strings.Contains(without, "pull request #") {
		t.Error("the target paragraph renders for a default-branch run")
	}
	for _, mode := range []string{"deploy", "teardown"} {
		if !strings.Contains(renderRun(t, mode, runParams{RepoName: "r", Name: "gke-pr42"}), "target.env") {
			t.Errorf("the %s prompt does not know a run can be pinned to a pull request", mode)
		}
	}
}
