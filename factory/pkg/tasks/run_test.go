package tasks

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
		if want := "docs-exploration/agent-runs/deploy-gke-k8s1/"; !strings.Contains(got, want) {
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
		"docs-exploration/runbook-deployments docs-exploration/runs",
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
	// "runs/" is a bare .gitignore entry in a great many repos
	// (TensorBoard writes there) and a bare pattern matches at any
	// depth, so docs-exploration/runs/ was ignored wherever it
	// appeared — silently, because git add's refusal was suppressed.
	if strings.Contains(s, `RUN_DIR="docs-exploration/runs/`) {
		t.Error("run directory is back under runs/, which repositories commonly gitignore")
	}
	// A staging failure must be loud. Swallowing it turns a completed
	// run into "nothing to commit" with the work already done.
	if strings.Contains(s, `git add --ignore-removal "${RUN_DIR}" 2>/dev/null`) {
		t.Error("git add errors are suppressed again; an ignored path would vanish the run")
	}
}

// Runs live on research/runs, and the repo-agent read path hardcodes
// the same string. Nothing links the two, so a change on either side
// that is not made on both empties the Runs tab of a member whose runs
// are perfectly intact on a branch nobody looks at.
func TestRunScriptWritesToTheRunsBranch(t *testing.T) {
	b, err := GetRunScript()
	if err != nil {
		t.Fatalf("GetRunScript: %v", err)
	}
	s := string(b)
	if !strings.Contains(s, `RUNS_BRANCH="research/runs"`) {
		t.Error(`run.sh does not set RUNS_BRANCH="research/runs"; repo-agent reads that name`)
	}
	// Checkout and push have to name the same variable. A literal
	// anywhere in the pair is how the two drift apart.
	for _, want := range []string{
		`git checkout -B "${RUNS_BRANCH}"`,
		`git push origin "${RUNS_BRANCH}"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("run.sh missing %q", want)
		}
	}
	// research/notes belongs to the research write path, which is a
	// different writer with a different lifetime: notes are archival and
	// the runs read path prunes what it finds. A run reaching that
	// branch would put live teardown scripts behind the delete path that
	// clears runs, and hand an auto-approving research session a push
	// that lands next to deployment state.
	if strings.Contains(s, "research/notes") {
		t.Error("run.sh touches the notes branch; runs and notes are kept apart on purpose")
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

// Plan absorbs what a separate draft mode would have done: the
// feasibility probing, and stopping at the procedure when the
// parameters cannot be resolved. Nothing executes at plan time, so
// there was never a reason for an earlier stopping point.
func TestPlanPromptVerifiesFeasibility(t *testing.T) {
	got := renderRun(t, "plan", RunParams{RepoName: "open-rl", Name: "deploy-gke-k8s1"})
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

// A repo with no GCP project configured should still get a readable
// procedure and a list of what it needs — not scripts built on
// invented values.
func TestPlanDegradesWhenParametersCannotResolve(t *testing.T) {
	got := renderRun(t, "plan", RunParams{RepoName: "open-rl", Name: "deploy-gke-k8s1"})
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

// There is no draft mode. If one reappears, the command surface and
// the stage count grew back.
func TestThereIsNoDraftMode(t *testing.T) {
	if _, err := RenderRunPrompt("draft", RunParams{}); err == nil {
		t.Error("draft resolved to a prompt; plan is supposed to do the drafting")
	}
}

// An empty RUN_NAME would make the legacy path the whole deployments
// tree, moving every instance at once.
func TestAdoptionRefusesAnEmptyRunName(t *testing.T) {
	b, err := GetRunScript()
	if err != nil {
		t.Fatalf("GetRunScript: %v", err)
	}
	if !strings.Contains(string(b), `[ -z "${RUN_NAME}" ]`) {
		t.Error("adoptLegacyInstance does not guard against an empty run name")
	}
}

// Observed live: a teardown edited .gitignore to get at its own files
// and wandered into a shared doc. Neither is inside the run's
// directory, so `git add ${RUN_DIR}` never staged them — and the
// leftover modifications made the push-race replay fail with "cannot
// rebase: You have unstaged changes", turning a recoverable race into
// a lost receipt.
func TestRunScriptLeavesNothingOutsideItsOwnDirectory(t *testing.T) {
	b, err := GetRunScript()
	if err != nil {
		t.Fatalf("GetRunScript: %v", err)
	}
	s := string(b)
	if !strings.Contains(s, "git checkout -f -- .") {
		t.Error("run.sh does not discard changes outside the run directory")
	}
	// Belt and braces: even a clean-tree check can race, so the replay
	// must survive a dirty worktree on its own.
	if !strings.Contains(s, `git rebase --autostash`) {
		t.Error("the push-race replay is not autostashing; an unstaged file will block it")
	}
}

// instantiateRunbook is executed, not grepped: every string test above
// passed while --from read a path runs had moved away from, and the
// copy under it held two set -e traps nobody had ever reached. It runs
// here the way run.sh runs it — under set -e, against a real git
// repository, since a repository runbook is read at a commit.
func TestInstantiateRunbook(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash required")
	}
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git required")
	}
	b, err := GetRunScript()
	if err != nil {
		t.Fatalf("GetRunScript: %v", err)
	}
	s := string(b)
	legacy := regexp.MustCompile(`(?m)^LEGACY_RUN_DIRS=.*$`).FindString(s)
	from := strings.Index(s, "RUNBOOK_REF=\"\"\n")
	fnAt := strings.Index(s, "function instantiateRunbook {")
	if legacy == "" || from < 0 || fnAt < 0 || fnAt < from {
		t.Fatal("run.sh is missing LEGACY_RUN_DIRS, RUNBOOK_REF or instantiateRunbook")
	}
	body := s[from : fnAt+strings.Index(s[fnAt:], "\n}\n")+3]

	const runs = "docs-exploration/agent-runs"
	write := func(t *testing.T, root, rel, content string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	git := func(t *testing.T, root string, args ...string) {
		t.Helper()
		cmd := exec.Command(gitBin, append([]string{"-C", root}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	repo := func(t *testing.T) string {
		root := t.TempDir()
		git(t, root, "init", "-q", "-b", "main")
		return root
	}
	commit := func(t *testing.T, root string) {
		git(t, root, "add", "-A")
		git(t, root, "commit", "-qm", "x")
	}
	// instantiate runs the function as run.sh does and reports whether
	// the script lived past it. ref is RUNBOOK_REF: "" is a failed fetch.
	instantiate := func(t *testing.T, root, runbook, ref, intent string) (string, bool) {
		t.Helper()
		script := "set -e\nset -o pipefail\nRUNS_BRANCH=research/runs\n" + legacy + "\n" +
			strings.ReplaceAll(body, "/workspaces/${REPO_NAME}", root) +
			"RUNBOOK_REF='" + ref + "'\ninstantiateRunbook\necho SURVIVED\n"
		cmd := exec.Command(bash, "-c", script)
		cmd.Env = append(os.Environ(),
			"RUN_NAME=pr-42", "RUN_DIR="+runs+"/pr-42", "RUN_RUNBOOK="+runbook, "RUN_INTENT="+intent,
			"RUN_RESOURCE_PREFIX=ab-pr-42", "GOOGLE_CLOUD_PROJECT=my-proj", "CLOUDSDK_COMPUTE_REGION=us-east1")
		out, _ := cmd.CombinedOutput()
		return string(out), strings.Contains(string(out), "SURVIVED")
	}
	read := func(root, rel string) string {
		b, _ := os.ReadFile(filepath.Join(root, rel))
		return string(b)
	}
	receipt := func(t *testing.T, root string) string {
		t.Helper()
		m, _ := filepath.Glob(filepath.Join(root, runs, "pr-42", "receipt-*.md"))
		if len(m) != 1 {
			t.Fatalf("want exactly one receipt, got %v", m)
		}
		return read(root, strings.TrimPrefix(m[0], root+"/"))
	}
	// gke writes a complete runbook under dir: the shape a plan leaves.
	gke := func(t *testing.T, root, dir string) {
		write(t, root, dir+"/runbook.md", "procedure")
		write(t, root, dir+"/params.env", "PROJECT=\"their-proj\"  # theirs\nREGION=us-central1\nRESOURCE_PREFIX=\"ab-gke\"\nCLUSTER=\"ab-gke-cluster\"\nNODES=3")
		write(t, root, dir+"/deploy.sh", "source params.env\ngcloud container clusters create ${CLUSTER}")
		write(t, root, dir+"/teardown.sh", "source params.env\ngcloud container clusters delete ${CLUSTER}")
	}

	t.Run("a repository runbook lands PLANNED, with this run's parameters", func(t *testing.T) {
		root := repo(t)
		gke(t, root, ".agents/runbooks/gke")
		commit(t, root)
		out, ok := instantiate(t, root, "gke", "main", "")
		if !ok {
			t.Fatalf("instantiateRunbook killed the script:\n%s", out)
		}
		params := read(root, runs+"/pr-42/params.env")
		for _, want := range []string{`RESOURCE_PREFIX="ab-pr-42"`, `PROJECT="my-proj"`, `REGION="us-east1"`, `CLUSTER="ab-pr-42-cluster"`, "NODES=3"} {
			if !strings.Contains(params, want) {
				t.Errorf("params.env lacks %s:\n%s", want, params)
			}
		}
		// The source's prefix surviving anywhere in params.env is a
		// deploy onto — and a teardown of — the source's resources.
		if strings.Contains(params, "ab-gke") || strings.Contains(params, "their-proj") {
			t.Errorf("params.env still names the source's resources:\n%s", params)
		}
		if got := read(root, runs+"/pr-42/deploy.sh"); !strings.Contains(got, "clusters create ${CLUSTER}") {
			t.Errorf("deploy.sh was not copied unchanged: %q", got)
		}
		r := receipt(t, root)
		if !strings.HasPrefix(r, "PLANNED") {
			t.Errorf("receipt verdict is not PLANNED:\n%s", r)
		}
		if !strings.Contains(r, ".agents/runbooks/gke at main") {
			t.Errorf("receipt does not say where the runbook came from:\n%s", r)
		}
	})

	t.Run("a repository runbook is read at the commit, not the worktree", func(t *testing.T) {
		// research/runs merges the default branch with -X ours, so the
		// worktree can hold the fork's copy of a runbook edited on both
		// sides. The repository's is the one that was reviewed.
		root := repo(t)
		gke(t, root, ".agents/runbooks/gke")
		commit(t, root)
		write(t, root, ".agents/runbooks/gke/runbook.md", "worktree edit")
		instantiate(t, root, "gke", "main", "")
		if got := read(root, runs+"/pr-42/runbook.md"); got != "procedure\n" {
			t.Errorf("runbook.md = %q, want the committed one", got)
		}
	})

	t.Run("the repository wins over a run of the same name", func(t *testing.T) {
		root := repo(t)
		gke(t, root, ".agents/runbooks/gke")
		commit(t, root)
		write(t, root, runs+"/gke/runbook.md", "my run")
		instantiate(t, root, "gke", "main", "")
		if got := read(root, runs+"/pr-42/runbook.md"); got != "procedure\n" {
			t.Errorf("runbook.md = %q, want the repository's", got)
		}
	})

	t.Run("a run is a runbook, and its receipts stay behind", func(t *testing.T) {
		root := repo(t)
		write(t, root, "README", "x")
		commit(t, root)
		gke(t, root, runs+"/gke")
		write(t, root, runs+"/gke/receipt-20260928-1200.md", "VERIFIED")
		out, ok := instantiate(t, root, "gke", "main", "")
		if !ok {
			t.Fatalf("instantiateRunbook killed the script:\n%s", out)
		}
		if !strings.HasPrefix(receipt(t, root), "PLANNED") {
			t.Error("a run used as a runbook did not land PLANNED")
		}
	})

	t.Run("a run still under a legacy path is found", func(t *testing.T) {
		root := repo(t)
		write(t, root, "README", "x")
		commit(t, root)
		gke(t, root, "docs-exploration/runs/gke")
		if out, ok := instantiate(t, root, "gke", "main", ""); !ok {
			t.Fatalf("instantiateRunbook killed the script:\n%s", out)
		}
		if got := read(root, runs+"/pr-42/runbook.md"); got != "procedure\n" {
			t.Errorf("runbook.md = %q, want the legacy run's", got)
		}
	})

	t.Run("a script naming the source's resources BLOCKs the run", func(t *testing.T) {
		// Scripts are copied, never rewritten: a substitution nobody
		// reviewed is not something to trust with a teardown.
		root := repo(t)
		write(t, root, "README", "x")
		commit(t, root)
		write(t, root, runs+"/gke/runbook.md", "procedure")
		write(t, root, runs+"/gke/params.env", "PROJECT=\"their-proj\"\nRESOURCE_PREFIX=\"ab-gke\"")
		write(t, root, runs+"/gke/deploy.sh", "gcloud --project their-proj compute instances create ab-gke-vm")
		out, ok := instantiate(t, root, "gke", "main", "")
		if !ok {
			t.Fatalf("instantiateRunbook killed the script:\n%s", out)
		}
		r := receipt(t, root)
		if !strings.HasPrefix(r, "BLOCKED") {
			t.Fatalf("receipt verdict is not BLOCKED:\n%s", r)
		}
		for _, want := range []string{"## Needs from owner", "`ab-gke`", "`their-proj`", "no teardown.sh"} {
			if !strings.Contains(r, want) {
				t.Errorf("receipt does not name %s:\n%s", want, r)
			}
		}
	})

	t.Run("a parameter nobody can fill BLOCKs the run", func(t *testing.T) {
		root := repo(t)
		gke(t, root, ".agents/runbooks/gke")
		write(t, root, ".agents/runbooks/gke/params.env", "RESOURCE_PREFIX=\"\"\nMACHINE_TYPE=\"\"")
		commit(t, root)
		instantiate(t, root, "gke", "main", "")
		r := receipt(t, root)
		if !strings.HasPrefix(r, "BLOCKED") || !strings.Contains(r, "MACHINE_TYPE is empty") {
			t.Errorf("an empty parameter did not BLOCK the run:\n%s", r)
		}
	})

	t.Run("with an intent the copy is made and the engine writes the receipt", func(t *testing.T) {
		root := repo(t)
		gke(t, root, ".agents/runbooks/gke")
		commit(t, root)
		if out, ok := instantiate(t, root, "gke", "main", "5 nodes"); !ok {
			t.Fatalf("instantiateRunbook killed the script:\n%s", out)
		}
		if !strings.Contains(read(root, runs+"/pr-42/params.env"), `RESOURCE_PREFIX="ab-pr-42"`) {
			t.Error("params.env was not rewritten before the engine plans")
		}
		if m, _ := filepath.Glob(filepath.Join(root, runs, "pr-42", "receipt-*.md")); len(m) != 0 {
			t.Errorf("a receipt was written though the engine is about to plan: %v", m)
		}
	})

	t.Run("a failed fetch falls back to the worktree", func(t *testing.T) {
		root := repo(t)
		gke(t, root, ".agents/runbooks/gke")
		if out, ok := instantiate(t, root, "gke", "", ""); !ok {
			t.Fatalf("instantiateRunbook killed the script:\n%s", out)
		}
		if r := receipt(t, root); !strings.Contains(r, "(working tree)") {
			t.Errorf("receipt does not say the runbook came from the worktree:\n%s", r)
		}
	})

	t.Run("an unknown runbook fails loudly and creates nothing", func(t *testing.T) {
		root := repo(t)
		write(t, root, "README", "x")
		commit(t, root)
		out, ok := instantiate(t, root, "nope", "main", "")
		if ok {
			t.Fatalf("an unknown runbook did not fail the run:\n%s", out)
		}
		if !strings.Contains(out, "no runbook named 'nope'") {
			t.Errorf("no explanation:\n%s", out)
		}
		if _, err := os.Stat(filepath.Join(root, runs, "pr-42")); err == nil {
			t.Error("a run directory was created for a runbook that does not exist")
		}
	})

	t.Run("an existing run is never overwritten", func(t *testing.T) {
		root := repo(t)
		gke(t, root, ".agents/runbooks/gke")
		commit(t, root)
		write(t, root, runs+"/pr-42/runbook.md", "mine")
		if _, ok := instantiate(t, root, "gke", "main", ""); ok {
			t.Error("instantiating over an existing run did not fail")
		}
		if got := read(root, runs+"/pr-42/runbook.md"); got != "mine\n" {
			t.Errorf("runbook.md = %q; an existing run's procedure was overwritten", got)
		}
	})
}

// With --intent the engine plans on top of the copy, so it has to know
// the copy is there and that its prefix is not to be touched.
func TestPlanPromptKnowsItStartedFromARunbook(t *testing.T) {
	with := renderRun(t, "plan", RunParams{RepoName: "r", Name: "pr-42", Intent: "5 nodes", Runbook: "gke"})
	for _, want := range []string{`started from the runbook "gke"`, "Keep RESOURCE_PREFIX exactly"} {
		if !strings.Contains(with, want) {
			t.Errorf("plan prompt missing %q", want)
		}
	}
	if without := renderRun(t, "plan", RunParams{RepoName: "r", Name: "pr-42"}); strings.Contains(without, "started from the runbook") {
		t.Error("the runbook paragraph renders for a run started from an intent")
	}
}
