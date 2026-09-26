package tasks

import (
	"os/exec"
	"strings"
	"testing"
)

func renderRun(t *testing.T, mode string, p RunParams) string {
	t.Helper()
	b, err := RenderRunPrompt(mode, p)
	if err != nil {
		t.Fatalf("RenderRunPrompt(%s): %v", mode, err)
	}
	return string(b)
}

func TestRunPromptsCarryTheRunDirectory(t *testing.T) {
	for _, mode := range []string{"plan", "deploy", "teardown"} {
		got := renderRun(t, mode, RunParams{RepoName: "open-rl", Name: "deploy-gke-k8s1"})
		if want := "docs-exploration/runs/deploy-gke-k8s1/"; !strings.Contains(got, want) {
			t.Errorf("%s prompt does not name %s", mode, want)
		}
		// A run owns one directory. Any mention of the old shared
		// runbook tree means the split we just removed leaked back in.
		if strings.Contains(got, "docs-exploration/runbooks/") {
			t.Errorf("%s prompt still points at the shared runbook tree", mode)
		}
	}
}

// The order is the contract: prose first, shell transcribed from it.
// Reversed, runbook.md becomes a summary of the scripts and drifts.
func TestPlanPromptOrdersProseBeforeShell(t *testing.T) {
	got := renderRun(t, "plan", RunParams{RepoName: "open-rl", Name: "deploy-gke-k8s1"})
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

// The deploy phase is the only thing that corrects the procedure, and
// it must do so whether or not the deploy worked: the scripts changed,
// so the prose that describes them has to change too.
func TestDeployPromptReconcilesTheProcedure(t *testing.T) {
	got := renderRun(t, "deploy", RunParams{RepoName: "open-rl", Name: "deploy-gke-k8s1"})
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
	with := renderRun(t, "plan", RunParams{RepoName: "r", Name: "n", Intent: "3 ubuntu nodes"})
	if !strings.Contains(with, "3 ubuntu nodes") {
		t.Error("intent not rendered")
	}
	without := renderRun(t, "plan", RunParams{RepoName: "r", Name: "n"})
	if strings.Contains(without, "What the owner asked for") {
		t.Error("empty intent still rendered its heading")
	}
}

func TestUnknownModeIsRejected(t *testing.T) {
	if _, err := RenderRunPrompt("execute", RunParams{}); err == nil {
		t.Error("expected an error for a mode that does not exist")
	}
}

// The script is assembled by concatenating lib.sh, so a syntax error
// surfaces only in the sandbox unless something checks it here.
func TestRunScriptParses(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	b, err := GetRunScript()
	if err != nil {
		t.Fatalf("GetRunScript: %v", err)
	}
	cmd := exec.Command("bash", "-n")
	cmd.Stdin = strings.NewReader(string(b))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("run.sh does not parse: %v\n%s", err, out)
	}
}

// Legacy deployments created by `factory runbook` live under
// runbook-deployments/<instance>/. They are adopted lazily, at the
// moment someone touches the run, rather than by a big-bang migration
// of live infrastructure.
func TestRunScriptAdoptsLegacyDeployments(t *testing.T) {
	b, err := GetRunScript()
	if err != nil {
		t.Fatalf("GetRunScript: %v", err)
	}
	s := string(b)
	for _, want := range []string{
		"adoptLegacyInstance",
		"docs-exploration/runbook-deployments/${RUN_NAME}",
		// --ignore-removal will not record the old path vanishing, so
		// the removal has to be staged explicitly or the branch keeps
		// both copies.
		`git add -A "${legacy}"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("run.sh missing %q", want)
		}
	}
	// A bare `[ … ] && return 0` aborts the whole script under set -e
	// when the test fails.
	if strings.Contains(s, `[ -d "${RUN_DIR}" ] && return 0`) {
		t.Error("guard written as a bare && list; under set -e a false test kills the run")
	}
}

// A re-plan is the same command with the same flag, so the prompt has
// to tell the model how to read a terse refinement. "5 nodes" is an
// amendment to an existing plan, not a brief that silently drops the
// cluster, the IAM grants and the build.
func TestPlanPromptDistinguishesAmendmentFromBrief(t *testing.T) {
	got := renderRun(t, "plan", RunParams{RepoName: "r", Name: "n", Intent: "5 nodes"})
	for _, want := range []string{"AMENDMENT to it", "this is the whole brief"} {
		if !strings.Contains(got, want) {
			t.Errorf("plan prompt missing %q — a refinement could be read as a replacement brief", want)
		}
	}
}

// Planning does not remove infrastructure, but a PLANNED receipt is
// the newest thing anyone reads. If it drops the running-resources
// list, a live cluster goes invisible and bills until noticed.
func TestPlanReceiptCarriesRunningResourcesForward(t *testing.T) {
	got := renderRun(t, "plan", RunParams{RepoName: "r", Name: "n"})
	for _, want := range []string{"Currently deployed", "left\n   RUNNING", "has not been applied"} {
		if !strings.Contains(got, want) {
			t.Errorf("plan prompt missing %q", want)
		}
	}
}

// Draft writes the procedure and stops. Emitting params or scripts
// there would resolve an account and a target before the owner has
// chosen either, which is what plan is for.
func TestDraftPromptWritesProcedureOnly(t *testing.T) {
	got := renderRun(t, "draft", RunParams{RepoName: "open-rl", Name: "deploy-gke"})
	for _, want := range []string{
		"docs-exploration/runs/deploy-gke/",
		"DRAFTED on the first line",
		"ACCOUNT-AGNOSTIC",
		"VERIFY FEASIBILITY",
		"Do NOT write params.env, deploy.sh or teardown.sh",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("draft prompt missing %q", want)
		}
	}
	if strings.Contains(got, "docs-exploration/runbooks/") {
		t.Error("draft prompt still points at the shared runbook tree")
	}
}

// Drafting has no name to scope the move, and an empty RUN_NAME would
// make the legacy path the whole deployments tree — moving every
// instance at once.
func TestAdoptionRefusesAnEmptyRunName(t *testing.T) {
	b, err := GetRunScript()
	if err != nil {
		t.Fatalf("GetRunScript: %v", err)
	}
	if !strings.Contains(string(b), `if [ -z "${RUN_NAME}" ]; then`) {
		t.Error("adoptLegacyInstance does not guard against an empty run name")
	}
}
