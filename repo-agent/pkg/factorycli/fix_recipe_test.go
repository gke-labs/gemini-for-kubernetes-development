package factorycli

import (
	"strings"
	"testing"
)

const changeTaskOutput = `apiVersion: factory.gemini.google.com/v1alpha1
kind: Change
target:
  url: https://github.com/o/repo/issues/5
  commit: 0123abcd
spec:
  fork: alice/repo
  branch: issue-5-1
  title: "fix the nil case"
`

// The script's apply proves it was handed the Change the run wrote.
const changeFactory = `recipe) echo "Running recipe..." ;;
sandbox) echo "Waiting for sandbox pod fix-repo-5 to become ready..." >&2; cat <<'DOC'
` + changeTaskOutput + `DOC
;;
apply) grep -q "kind: Change" "$3" && echo "Opened https://github.com/o/repo/pull/9" ;;
*) exit 9 ;;`

// The fix runs as a recipe, is read back by run name and opened as a
// draft PR with open-pr, in the member's namespace: open-pr finds the
// issue's sandbox there to alias to the PR.
func TestStartFixRunsTheRecipeAndOpensThePR(t *testing.T) {
	bin, argsLog := fakeFactory(t, changeFactory)
	p := &fakeProber{probe: TaskProbe{State: ProbeRunning}}
	r := &Runner{Binary: bin, Prober: p, running: map[string]struct{}{}, results: map[string]Result{}}
	r.StartFix("alice/fix-repo-5", FixOptions{
		Namespace: "alice", SandboxName: "fix-repo-5", IssueURL: "https://github.com/o/repo/issues/5",
		RunName: "fix/b/5/1", WithPlan: true, Engine: "claude", Image: "img:1", WorkspaceDiskSize: "20Gi", Disclose: true,
	})
	res := waitResult(t, r, "alice/fix-repo-5")
	if res.Err != nil {
		t.Fatalf("run: %v\n%s", res.Err, res.Output)
	}
	if !strings.Contains(res.Output, changeBanner+"\n"+changeTaskOutput) || !strings.Contains(res.Output, "Opened https://github.com/o/repo/pull/9") {
		t.Errorf("output = %q, want the Change and what apply said", res.Output)
	}
	args := readArgs(t, argsLog)
	if len(args) != 3 {
		t.Fatalf("commands run = %q, want recipe, task output, apply", args)
	}
	for _, want := range []string{"--url https://github.com/o/repo/issues/5", "--namespace alice", "--abort-on-cancel=false",
		"--with-plan true", "--engine claude", "--image img:1", "--workspace-disk-size 20Gi", "--disclose=true"} {
		if !strings.HasPrefix(args[0], "recipe fix --run-name fix/b/5/1 ") || !strings.Contains(args[0], want) {
			t.Errorf("recipe args = %q, want %q", args[0], want)
		}
	}
	if strings.Contains(args[0], "--instruction") {
		t.Errorf("recipe args = %q: the fix takes no draft-PR instruction, open-pr opens a draft", args[0])
	}
	if !strings.HasPrefix(args[1], "sandbox task output fix-repo-5 --namespace alice --run-name fix/b/5/1") {
		t.Errorf("output args = %q", args[1])
	}
	if !strings.HasPrefix(args[2], "apply -f ") || !strings.Contains(args[2], "--action open-pr") || !strings.Contains(args[2], "--namespace alice") {
		t.Errorf("apply args = %q", args[2])
	}
	if p.taskType != "" {
		t.Errorf("probed %q: a fix is resumed by its run name", p.taskType)
	}
}

// A fix whose PR cannot be opened fails, with what apply said.
func TestStartFixOpenPRFailure(t *testing.T) {
	bin, _ := fakeFactory(t, strings.Replace(changeFactory, `apply) grep -q "kind: Change" "$3" && echo "Opened https://github.com/o/repo/pull/9" ;;`,
		`apply) echo "Error: the fork has no branch issue-5-1"; exit 1 ;;`, 1))
	r := &Runner{Binary: bin, running: map[string]struct{}{}, results: map[string]Result{}}
	r.StartFix("alice/fix-repo-5", FixOptions{Namespace: "alice", SandboxName: "fix-repo-5", IssueURL: "u", RunName: "fix/b/5/1"})
	res := waitResult(t, r, "alice/fix-repo-5")
	if res.Err == nil || !strings.Contains(res.Output, "Error: the fork has no branch") {
		t.Fatalf("want apply's failure, got %v\n%s", res.Err, res.Output)
	}
}

// A fix's follow-up revises in the fix's session with its inputs, and
// posts the replies its Change carries.
func TestStartRevisePostsReplies(t *testing.T) {
	bin, argsLog := fakeFactory(t, changeFactory)
	r := &Runner{Binary: bin, running: map[string]struct{}{}, results: map[string]Result{}}
	r.StartRevise("alice/revise-fix-repo-5", ReviseOptions{
		Namespace: "alice", SandboxName: "fix-repo-5", Revise: "iterate", Session: "recipe-fix-1",
		RunName: "revise/b/fix-repo-5/iterate/1", PostReplies: true,
		Inputs: map[string]string{"instruction": "rename it", "a": "b"},
	})
	res := waitResult(t, r, "alice/revise-fix-repo-5")
	if res.Err != nil {
		t.Fatalf("run: %v\n%s", res.Err, res.Output)
	}
	args := readArgs(t, argsLog)
	if len(args) != 3 || !strings.HasPrefix(args[0], "recipe revise fix-repo-5 iterate ") {
		t.Fatalf("commands run = %q, want revise, task output, apply", args)
	}
	if !strings.Contains(args[0], "--task recipe-fix-1 --input a=b --input instruction=rename it") {
		t.Errorf("revise args = %q, want the session and the inputs, sorted", args[0])
	}
	if !strings.Contains(args[2], "--action post-replies") || !strings.Contains(args[2], "--namespace alice") {
		t.Errorf("apply args = %q", args[2])
	}
}
