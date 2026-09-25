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
