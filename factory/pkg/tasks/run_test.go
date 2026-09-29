package tasks

import (
	"encoding/json"
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
	// instantiateAt runs the function as run.sh does and reports whether
	// the script lived past it. ref is RUNBOOK_REF: "" is a failed fetch;
	// fallback is RUNBOOK_FALLBACK_REF, set by a pull request target.
	instantiateAt := func(t *testing.T, root, runbook, ref, fallback string) (string, bool) {
		t.Helper()
		script := "set -e\nset -o pipefail\nRUNS_BRANCH=research/runs\n" + legacy + "\n" +
			strings.ReplaceAll(body, "/workspaces/${REPO_NAME}", root) +
			"RUNBOOK_REF='" + ref + "'\nRUNBOOK_FALLBACK_REF='" + fallback + "'\n" +
			"instantiateRunbook\necho \"SURVIVED ${RUNBOOK_ORIGIN}\"\n"
		cmd := exec.Command(bash, "-c", script)
		cmd.Env = append(os.Environ(), "RUN_NAME=pr-42", "RUN_DIR="+runs+"/pr-42", "RUN_RUNBOOK="+runbook)
		out, _ := cmd.CombinedOutput()
		return string(out), strings.Contains(string(out), "SURVIVED")
	}
	instantiate := func(t *testing.T, root, runbook, ref string) (string, bool) {
		t.Helper()
		return instantiateAt(t, root, runbook, ref, "")
	}
	read := func(root, rel string) string {
		b, _ := os.ReadFile(filepath.Join(root, rel))
		return string(b)
	}
	copied := func(t *testing.T, root string) []string {
		t.Helper()
		var got []string
		dir := filepath.Join(root, runs, "pr-42")
		_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() {
				got = append(got, strings.TrimPrefix(p, dir+"/"))
			}
			return nil
		})
		return got
	}

	t.Run("a repository runbook is copied whole, as it is", func(t *testing.T) {
		// No required shape: prose alone, or with scripts and whatever
		// they read — the plan makes the run's files out of it.
		root := repo(t)
		write(t, root, ".agents/runbooks/gke/README.md", "procedure")
		write(t, root, ".agents/runbooks/gke/manifests/values.yaml", "nodes: 3")
		write(t, root, ".agents/runbooks/gke/params.env", `RESOURCE_PREFIX="ab-gke"`)
		commit(t, root)
		out, ok := instantiate(t, root, "gke", "main")
		if !ok {
			t.Fatalf("instantiateRunbook killed the script:\n%s", out)
		}
		if got := strings.Join(copied(t, root), " "); got != "README.md manifests/values.yaml params.env" {
			t.Errorf("copied %q", got)
		}
		// Nothing is rewritten here; making it this run's is the plan's.
		if got := read(root, runs+"/pr-42/params.env"); got != "RESOURCE_PREFIX=\"ab-gke\"\n" {
			t.Errorf("params.env was changed by the copy: %q", got)
		}
		if !strings.Contains(out, "SURVIVED .agents/runbooks/gke at main") {
			t.Errorf("the origin is not recorded:\n%s", out)
		}
	})

	t.Run("a repository runbook is read at the commit, not the worktree", func(t *testing.T) {
		// research/runs merges the default branch with -X ours, so the
		// worktree can hold the fork's copy of a runbook edited on both
		// sides. The repository's is the one that was reviewed.
		root := repo(t)
		write(t, root, ".agents/runbooks/gke/runbook.md", "procedure")
		commit(t, root)
		write(t, root, ".agents/runbooks/gke/runbook.md", "worktree edit")
		instantiate(t, root, "gke", "main")
		if got := read(root, runs+"/pr-42/runbook.md"); got != "procedure\n" {
			t.Errorf("runbook.md = %q, want the committed one", got)
		}
	})

	t.Run("the repository wins over a run of the same name", func(t *testing.T) {
		root := repo(t)
		write(t, root, ".agents/runbooks/gke/runbook.md", "procedure")
		commit(t, root)
		write(t, root, runs+"/gke/runbook.md", "my run")
		instantiate(t, root, "gke", "main")
		if got := read(root, runs+"/pr-42/runbook.md"); got != "procedure\n" {
			t.Errorf("runbook.md = %q, want the repository's", got)
		}
	})

	t.Run("a run is a runbook, and its receipts stay behind", func(t *testing.T) {
		root := repo(t)
		write(t, root, "README", "x")
		commit(t, root)
		write(t, root, runs+"/gke/runbook.md", "my run")
		write(t, root, runs+"/gke/deploy.sh", "create")
		write(t, root, runs+"/gke/receipt-20260928-1200.md", "VERIFIED")
		out, ok := instantiate(t, root, "gke", "main")
		if !ok {
			t.Fatalf("instantiateRunbook killed the script:\n%s", out)
		}
		if got := strings.Join(copied(t, root), " "); got != "deploy.sh runbook.md" {
			t.Errorf("copied %q; another run's receipts are not this run's", got)
		}
	})

	t.Run("a repository runbook's receipts stay behind too", func(t *testing.T) {
		root := repo(t)
		write(t, root, ".agents/runbooks/gke/runbook.md", "procedure")
		write(t, root, ".agents/runbooks/gke/receipt-20260928-1200.md", "VERIFIED")
		commit(t, root)
		instantiate(t, root, "gke", "main")
		if got := strings.Join(copied(t, root), " "); got != "runbook.md" {
			t.Errorf("copied %q", got)
		}
	})

	t.Run("another run's pull request pin stays behind", func(t *testing.T) {
		// A copied target.env would have this run deploy that pull
		// request, whatever it was started for.
		root := repo(t)
		write(t, root, "README", "x")
		commit(t, root)
		write(t, root, runs+"/gke-pr7/runbook.md", "my run")
		write(t, root, runs+"/gke-pr7/target.env", "TARGET_PR=7")
		if out, ok := instantiate(t, root, "gke-pr7", "main"); !ok {
			t.Fatalf("instantiateRunbook killed the script:\n%s", out)
		}
		if got := strings.Join(copied(t, root), " "); got != "runbook.md" {
			t.Errorf("copied %q", got)
		}
	})

	t.Run("a run still under a legacy path is found", func(t *testing.T) {
		root := repo(t)
		write(t, root, "README", "x")
		commit(t, root)
		write(t, root, "docs-exploration/runs/gke/runbook.md", "legacy")
		if out, ok := instantiate(t, root, "gke", "main"); !ok {
			t.Fatalf("instantiateRunbook killed the script:\n%s", out)
		}
		if got := read(root, runs+"/pr-42/runbook.md"); got != "legacy\n" {
			t.Errorf("runbook.md = %q, want the legacy run's", got)
		}
	})

	t.Run("a pull request that does not carry the runbook gets the default branch's", func(t *testing.T) {
		// The runbooks on offer are the default branch's, and a pull
		// request opened before one merged does not have it at its head.
		root := repo(t)
		write(t, root, "README", "x")
		commit(t, root)
		git(t, root, "branch", "pr")
		write(t, root, ".agents/runbooks/gke/runbook.md", "procedure")
		commit(t, root)
		out, ok := instantiateAt(t, root, "gke", "pr", "main")
		if !ok {
			t.Fatalf("instantiateRunbook killed the script:\n%s", out)
		}
		if got := read(root, runs+"/pr-42/runbook.md"); got != "procedure\n" {
			t.Errorf("runbook.md = %q, want the default branch's", got)
		}
		if !strings.Contains(out, "SURVIVED .agents/runbooks/gke at main") {
			t.Errorf("the origin does not say it came from the default branch:\n%s", out)
		}
	})

	t.Run("a pull request that changes the runbook is deployed with its own", func(t *testing.T) {
		root := repo(t)
		write(t, root, ".agents/runbooks/gke/runbook.md", "procedure")
		commit(t, root)
		git(t, root, "checkout", "-q", "-b", "pr")
		write(t, root, ".agents/runbooks/gke/runbook.md", "changed by the pull request")
		commit(t, root)
		git(t, root, "checkout", "-q", "main")
		if out, ok := instantiateAt(t, root, "gke", "pr", "main"); !ok {
			t.Fatalf("instantiateRunbook killed the script:\n%s", out)
		}
		if got := read(root, runs+"/pr-42/runbook.md"); got != "changed by the pull request\n" {
			t.Errorf("runbook.md = %q, want the pull request's", got)
		}
	})

	t.Run("a failed fetch falls back to the worktree", func(t *testing.T) {
		root := repo(t)
		write(t, root, ".agents/runbooks/gke/runbook.md", "procedure")
		out, ok := instantiate(t, root, "gke", "")
		if !ok {
			t.Fatalf("instantiateRunbook killed the script:\n%s", out)
		}
		if !strings.Contains(out, "(working tree)") {
			t.Errorf("the origin does not say it came from the worktree:\n%s", out)
		}
	})

	t.Run("an unknown runbook fails loudly and creates nothing", func(t *testing.T) {
		root := repo(t)
		write(t, root, "README", "x")
		commit(t, root)
		out, ok := instantiate(t, root, "nope", "main")
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
		write(t, root, ".agents/runbooks/gke/runbook.md", "procedure")
		commit(t, root)
		write(t, root, runs+"/pr-42/runbook.md", "mine")
		if _, ok := instantiate(t, root, "gke", "main"); ok {
			t.Error("instantiating over an existing run did not fail")
		}
		if got := read(root, runs+"/pr-42/runbook.md"); got != "mine\n" {
			t.Errorf("runbook.md = %q; an existing run's procedure was overwritten", got)
		}
	})
}

// A run started from a runbook is always planned: the copy is the
// plan's starting point, never a plan by itself.
func TestRunbookIsAlwaysPlanned(t *testing.T) {
	b, err := GetRunScript()
	if err != nil {
		t.Fatalf("GetRunScript: %v", err)
	}
	s := string(b)
	at := strings.Index(s, "instantiateRunbook\n")
	if at < 0 {
		t.Fatal("the plan case does not instantiate the runbook")
	}
	next := s[at : at+200]
	if !strings.Contains(next, "runEngine") || strings.Index(next, "runEngine") > strings.Index(next, "commitAndPushRun") {
		t.Errorf("the copy is committed without the engine planning it:\n%s", next)
	}
	for _, gone := range []string{"rewriteParams", "RUNBOOK_FILES"} {
		if strings.Contains(s, gone) {
			t.Errorf("run.sh still has %s: making the copy this run's is the plan's job", gone)
		}
	}
}

// The engine has to know the copy is there, that it makes it this
// run's, and that it is not to redesign it.
func TestPlanPromptKnowsItStartedFromARunbook(t *testing.T) {
	with := renderRun(t, "plan", RunParams{RepoName: "r", Name: "pr-42", Runbook: "gke"})
	for _, want := range []string{`started from the runbook "gke"`, "RESOURCE_PREFIX is this run's", "Change nothing else.", "verify it here"} {
		if !strings.Contains(with, want) {
			t.Errorf("plan prompt missing %q", want)
		}
	}
	if amended := renderRun(t, "plan", RunParams{RepoName: "r", Name: "pr-42", Intent: "5 nodes", Runbook: "gke"}); !strings.Contains(amended, "Change nothing else beyond what the owner asked for above.") {
		t.Error("with an intent, the prompt does not allow the owner's changes")
	}
	if without := renderRun(t, "plan", RunParams{RepoName: "r", Name: "pr-42"}); strings.Contains(without, "started from the runbook") {
		t.Error("the runbook paragraph renders for a run started from nothing")
	}
}

// resolveTarget and clearTarget are executed against real repositories:
// an upstream with a pull request ref, and a workspace cloned from it
// with a run of its own, the way a sandbox has them.
func TestTargetIsPinnedAndLaidOverTheWorktree(t *testing.T) {
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
	from := strings.Index(s, "TARGET_PR=\"\"\n")
	to := strings.Index(s, "\nfunction commitAndPushRun {")
	if from < 0 || to < from {
		t.Fatal("run.sh is missing TARGET_PR or commitAndPushRun")
	}
	body := s[from:to]

	gitEnv := append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	git := func(t *testing.T, dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command(gitBin, append([]string{"-C", dir}, args...)...)
		cmd.Env = gitEnv
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
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
	read := func(root, rel string) string {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			return "<missing>"
		}
		return strings.TrimSpace(string(b))
	}
	const runDir = "docs-exploration/agent-runs/gke-pr42"

	// setup returns the upstream (main, and pull request 42 changing
	// app.txt, deleting old.txt, adding new.txt) and a workspace cloned
	// from it with the run's own directory committed.
	setup := func(t *testing.T) (up, ws, head string) {
		up = t.TempDir()
		git(t, up, "init", "-q", "-b", "main")
		write(t, up, "app.txt", "base")
		write(t, up, "old.txt", "old")
		git(t, up, "add", "-A")
		git(t, up, "commit", "-qm", "base")
		git(t, up, "checkout", "-qb", "pr")
		write(t, up, "app.txt", "pr")
		write(t, up, "new.txt", "new")
		git(t, up, "rm", "-q", "old.txt")
		git(t, up, "add", "-A")
		git(t, up, "commit", "-qm", "pr")
		head = git(t, up, "rev-parse", "HEAD")
		git(t, up, "update-ref", "refs/pull/42/head", head)
		git(t, up, "checkout", "-q", "main")

		ws = filepath.Join(t.TempDir(), "ws")
		git(t, filepath.Dir(ws), "clone", "-q", up, ws)
		git(t, ws, "remote", "rename", "origin", "upstream")
		write(t, ws, runDir+"/runbook.md", "procedure")
		git(t, ws, "add", "-A")
		git(t, ws, "commit", "-qm", "run")
		return up, ws, head
	}
	// run executes resolveTarget, then then, as run.sh would.
	run := func(t *testing.T, ws, mode, pr, then string) (string, bool) {
		t.Helper()
		script := "set -e\nset -o pipefail\nSRC_REMOTE=upstream\nRUNBOOK_REF=main\nTARGET_FILE=target.env\n" +
			strings.ReplaceAll(body, "/workspaces/${REPO_NAME}", ws) +
			"\nresolveTarget\n" + then + "\necho \"SURVIVED ${RUNBOOK_REF} ${RUNBOOK_FALLBACK_REF}\"\n"
		cmd := exec.Command(bash, "-c", script)
		cmd.Env = append(gitEnv, "RUN_DIR="+runDir, "RUN_MODE="+mode, "RUN_TARGET_PR="+pr)
		out, _ := cmd.CombinedOutput()
		return string(out), strings.Contains(string(out), "SURVIVED")
	}

	t.Run("a plan pins the head and checks the pull request out", func(t *testing.T) {
		_, ws, head := setup(t)
		out, ok := run(t, ws, "plan", "42", "")
		if !ok {
			t.Fatalf("resolveTarget killed the script:\n%s", out)
		}
		if got, want := read(ws, runDir+"/target.env"), "TARGET_PR=42\nTARGET_SHA="+head; got != want {
			t.Errorf("target.env = %q, want %q", got, want)
		}
		if read(ws, "app.txt") != "pr" || read(ws, "new.txt") != "new" || read(ws, "old.txt") != "<missing>" {
			t.Errorf("the worktree is not the pull request: app=%q new=%q old=%q", read(ws, "app.txt"), read(ws, "new.txt"), read(ws, "old.txt"))
		}
		if read(ws, runDir+"/runbook.md") != "procedure" {
			t.Error("the run's own directory was touched")
		}
		// A runbook the pull request changes is the one it runs with.
		if !strings.Contains(out, "SURVIVED "+head) {
			t.Errorf("RUNBOOK_REF is not the pinned commit:\n%s", out)
		}
		// And one it does not carry comes from the default branch.
		if !strings.Contains(out, "SURVIVED "+head+" main") {
			t.Errorf("RUNBOOK_FALLBACK_REF is not the default branch:\n%s", out)
		}
		// Nothing is staged: the pull request is never committed.
		if staged := git(t, ws, "diff", "--cached", "--name-only"); staged != "" {
			t.Errorf("staged %q", staged)
		}
	})

	t.Run("a deploy executes the pin, not wherever the pull request is now", func(t *testing.T) {
		up, ws, head := setup(t)
		if out, ok := run(t, ws, "plan", "42", "clearTarget"); !ok {
			t.Fatalf("plan: %s", out)
		}
		git(t, up, "checkout", "-q", "pr")
		write(t, up, "app.txt", "moved")
		git(t, up, "commit", "-qam", "moved")
		git(t, up, "update-ref", "refs/pull/42/head", "HEAD")
		out, ok := run(t, ws, "deploy", "", "")
		if !ok {
			t.Fatalf("resolveTarget killed the script:\n%s", out)
		}
		if got := read(ws, "app.txt"); got != "pr" {
			t.Errorf("app.txt = %q; the deploy did not execute the commit the plan pinned", got)
		}
		if !strings.Contains(out, head) {
			t.Errorf("the pinned commit is not named:\n%s", out)
		}
	})

	t.Run("clearing puts the branch back and names what the engine changed", func(t *testing.T) {
		_, ws, _ := setup(t)
		out, ok := run(t, ws, "plan", "42", "echo edited >> "+ws+"/app.txt\nclearTarget")
		if !ok {
			t.Fatalf("killed:\n%s", out)
		}
		if !strings.Contains(out, "  app.txt") || strings.Contains(out, "  new.txt") {
			t.Errorf("the collateral list is wrong — only app.txt was edited:\n%s", out)
		}
		if read(ws, "app.txt") != "base" || read(ws, "old.txt") != "old" || read(ws, "new.txt") != "<missing>" {
			t.Error("the worktree is not back to the branch")
		}
		if got := git(t, ws, "status", "--porcelain"); got != "?? "+runDir+"/target.env" {
			t.Errorf("status = %q; only the pin should be left to commit", got)
		}
	})

	t.Run("no pin is the default branch, untouched", func(t *testing.T) {
		_, ws, _ := setup(t)
		out, ok := run(t, ws, "deploy", "", "clearTarget")
		if !ok || !strings.Contains(out, "SURVIVED main") || read(ws, "app.txt") != "base" {
			t.Errorf("a run with no target changed something:\n%s", out)
		}
	})

	t.Run("a pin that is not a number and a commit is refused", func(t *testing.T) {
		_, ws, _ := setup(t)
		write(t, ws, runDir+"/target.env", "TARGET_PR=42; touch "+ws+"/pwned")
		out, ok := run(t, ws, "deploy", "", "")
		if ok || !strings.Contains(out, "does not pin a pull request and a commit") {
			t.Errorf("a malformed pin was not refused:\n%s", out)
		}
		if read(ws, "pwned") != "<missing>" {
			t.Error("target.env was executed")
		}
	})

	t.Run("a pinned commit that is gone says to re-plan", func(t *testing.T) {
		_, ws, _ := setup(t)
		write(t, ws, runDir+"/target.env", "TARGET_PR=42\nTARGET_SHA="+strings.Repeat("0", 40))
		out, ok := run(t, ws, "deploy", "", "")
		if ok || !strings.Contains(out, "Re-plan with --target 42") {
			t.Errorf("a missing commit was not explained:\n%s", out)
		}
	})

	t.Run("a pull request that does not exist fails the plan", func(t *testing.T) {
		_, ws, _ := setup(t)
		out, ok := run(t, ws, "plan", "99", "")
		if ok || !strings.Contains(out, "could not fetch pull request #99") {
			t.Errorf("a missing pull request did not fail:\n%s", out)
		}
		if read(ws, runDir+"/target.env") != "<missing>" {
			t.Error("a pin was written for a pull request that does not exist")
		}
	})
}

// Every commit goes through commitAndPushRun, so that is where the
// pull request has to come off the worktree; and resolveTarget has to
// run before anything reads the checkout.
func TestTargetIsClearedBeforeEveryCommit(t *testing.T) {
	b, err := GetRunScript()
	if err != nil {
		t.Fatalf("GetRunScript: %v", err)
	}
	s := string(b)
	fn := s[strings.Index(s, "function commitAndPushRun {"):]
	if c, a := strings.Index(fn, "clearTarget"), strings.Index(fn, "git add"); c < 0 || c > a {
		t.Error("commitAndPushRun stages before clearing the target")
	}
	if !strings.Contains(s, "ensureRunsBranch\nresolveTarget\nsnapshotReceipts\nconfigureGemini\n") {
		t.Error("resolveTarget does not run right after the runs branch is ready")
	}
}

func TestPlanPromptKnowsItsTarget(t *testing.T) {
	with := renderRun(t, "plan", RunParams{RepoName: "r", HTMLURL: "https://github.com/o/r", Name: "gke-pr42", Target: 42})
	for _, want := range []string{"deploys pull request #42 (https://github.com/o/r/pull/42)", "target.env", "do not edit it", "TARGET_SHA"} {
		if !strings.Contains(with, want) {
			t.Errorf("plan prompt missing %q", want)
		}
	}
	if without := renderRun(t, "plan", RunParams{RepoName: "r", Name: "gke"}); strings.Contains(without, "pull request #") {
		t.Error("the target paragraph renders for a default-branch run")
	}
	for _, mode := range []string{"deploy", "teardown"} {
		if !strings.Contains(renderRun(t, mode, RunParams{RepoName: "r", Name: "gke-pr42"}), "target.env") {
			t.Errorf("the %s prompt does not know a run can be pinned to a pull request", mode)
		}
	}
}

// runResult is result.json as a reader sees it.
type runResult struct {
	Version   int        `json:"version"`
	Run       string     `json:"run"`
	Mode      string     `json:"mode"`
	ExitCode  int        `json:"exitCode"`
	Verdict   *string    `json:"verdict"`
	Receipt   *string    `json:"receipt"`
	Branch    string     `json:"branch"`
	Commit    *string    `json:"commit"`
	Target    *runTarget `json:"target"`
	Discarded []string   `json:"discarded"`
}

type runTarget struct {
	PR  int    `json:"pr"`
	SHA string `json:"sha"`
}

// result.json is executed, not grepped: it is written from an EXIT
// trap under set -e, where a failing command inside the writer would
// take the exit code with it, and its one input from the engine is a
// receipt's first line in whatever markdown the engine chose.
func TestRunWritesItsResult(t *testing.T) {
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
	from := strings.Index(s, "TARGET_PR=\"\"\n")
	to := strings.Index(s, "\n# Main execution\n")
	trapAt := strings.Index(s, "\ntrap ")
	if from < 0 || to < from || trapAt < to {
		t.Fatal("run.sh is missing TARGET_PR, the main section or its EXIT trap")
	}
	trap := s[trapAt+1 : trapAt+1+strings.Index(s[trapAt+1:], "\n")]
	body := s[from:to]

	gitEnv := append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	git := func(t *testing.T, dir string, args ...string) {
		t.Helper()
		cmd := exec.Command(gitBin, append([]string{"-C", dir}, args...)...)
		cmd.Env = gitEnv
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	const runDir = "docs-exploration/agent-runs/r1"

	// run sets up a workspace whose runs branch already holds a PLANNED
	// receipt, then runs engine as the invocation, with the EXIT trap
	// run.sh installs. It returns the exit code and result.json.
	run := func(t *testing.T, engine string) (int, runResult, string) {
		t.Helper()
		root := t.TempDir()
		origin, ws, task := filepath.Join(root, "origin.git"), filepath.Join(root, "ws"), filepath.Join(root, "task")
		git(t, root, "init", "-q", "--bare", origin)
		git(t, root, "init", "-q", "-b", "research/runs", ws)
		if err := os.MkdirAll(filepath.Join(ws, runDir), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(task, 0755); err != nil {
			t.Fatal(err)
		}
		for rel, content := range map[string]string{"app.txt": "base", runDir + "/receipt-20260101-0000.md": "PLANNED"} {
			if err := os.WriteFile(filepath.Join(ws, rel), []byte(content+"\n"), 0644); err != nil {
				t.Fatal(err)
			}
		}
		git(t, ws, "add", "-A")
		git(t, ws, "commit", "-qm", "base")
		git(t, ws, "remote", "add", "origin", origin)
		git(t, ws, "push", "-q", "origin", "research/runs")

		script := "set -e\nset -o pipefail\nREPO_NAME=repo\nRUN_NAME=r1\nRUN_MODE=deploy\nRUNS_BRANCH=research/runs\nTARGET_FILE=target.env\n" +
			"RUN_DIR=" + runDir + "\nPROMPT_FILE=" + filepath.Join(task, "prompt.txt") + "\n" +
			strings.ReplaceAll(body, "/workspaces/${REPO_NAME}", ws) + "\n" + trap + "\nsnapshotReceipts\ncd " + ws + "\n" + engine + "\n"
		cmd := exec.Command(bash, "-c", script)
		cmd.Env = gitEnv
		out, _ := cmd.CombinedOutput()
		code := cmd.ProcessState.ExitCode()
		raw, err := os.ReadFile(filepath.Join(task, "result.json"))
		if err != nil {
			t.Fatalf("no result.json (exit %d):\n%s", code, out)
		}
		var res runResult
		if err := json.Unmarshal(raw, &res); err != nil {
			t.Fatalf("result.json is not JSON: %v\n%s\n%s", err, raw, out)
		}
		return code, res, string(out)
	}
	str := func(p *string) string {
		if p == nil {
			return "<null>"
		}
		return *p
	}

	t.Run("a run names its verdict, receipt, commit and what it threw away", func(t *testing.T) {
		code, res, out := run(t, `printf '\n**VERIFIED** — all good\n' > $RUN_DIR/receipt-20260929-0337.md
echo x > .gcloudignore
echo changed > app.txt
commitAndPushRun deploy`)
		if code != 0 {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		if res.Version != 1 || res.Run != "r1" || res.Mode != "deploy" || res.Branch != "research/runs" || res.ExitCode != 0 {
			t.Errorf("identity = %+v", res)
		}
		if str(res.Verdict) != "VERIFIED" {
			t.Errorf("verdict = %s, want VERIFIED (the first word, past the markdown)", str(res.Verdict))
		}
		if str(res.Receipt) != runDir+"/receipt-20260929-0337.md" {
			t.Errorf("receipt = %s, want this invocation's, not the plan's", str(res.Receipt))
		}
		if len(str(res.Commit)) != 40 {
			t.Errorf("commit = %s, want the pushed sha", str(res.Commit))
		}
		if strings.Join(res.Discarded, ",") != ".gcloudignore,app.txt" {
			t.Errorf("discarded = %v, want the created file as well as the edited one", res.Discarded)
		}
		if !strings.Contains(out, "Not committed (created outside "+runDir+"):") {
			t.Errorf("the created file went unmentioned in the log:\n%s", out)
		}
	})

	t.Run("a run that dies before its receipt still says so, and keeps its exit code", func(t *testing.T) {
		code, res, _ := run(t, "false")
		if code != 1 || res.ExitCode != 1 {
			t.Errorf("exit %d, exitCode %d, want 1 and 1", code, res.ExitCode)
		}
		if res.Verdict != nil || res.Receipt != nil || res.Commit != nil || res.Target != nil || len(res.Discarded) != 0 {
			t.Errorf("a run with nothing to show claims something: %+v", res)
		}
	})

	t.Run("a first line that is not a verdict is unknown, never guessed", func(t *testing.T) {
		code, res, _ := run(t, "echo 'Deployment went fine' > $RUN_DIR/receipt-20260929-0500-teardown.md\nexit 3")
		if code != 3 || res.ExitCode != 3 {
			t.Errorf("exit %d, exitCode %d, want 3 and 3", code, res.ExitCode)
		}
		if str(res.Verdict) != "unknown" {
			t.Errorf("verdict = %s, want unknown", str(res.Verdict))
		}
	})

	t.Run("a pull request run names its pin", func(t *testing.T) {
		_, res, _ := run(t, "echo TORN-DOWN > $RUN_DIR/receipt-20260929-0600-teardown.md\nTARGET_PR=42\nTARGET_SHA=abc")
		if res.Target == nil || res.Target.PR != 42 || res.Target.SHA != "abc" {
			t.Errorf("target = %+v, want pr 42 at abc", res.Target)
		}
		if str(res.Verdict) != "TORN-DOWN" {
			t.Errorf("verdict = %s, want TORN-DOWN", str(res.Verdict))
		}
	})

	t.Run("a path is escaped, not pasted into the JSON", func(t *testing.T) {
		_, res, _ := run(t, `addDiscarded 'we"ird\path'`)
		if strings.Join(res.Discarded, ",") != `we"ird\path` {
			t.Errorf("discarded = %q", res.Discarded)
		}
	})
}
