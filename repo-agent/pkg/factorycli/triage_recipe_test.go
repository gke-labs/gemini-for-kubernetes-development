package factorycli

import (
	"os"
	"strings"
	"testing"
)

const triageTaskOutput = `apiVersion: factory.gemini.google.com/v1alpha1
kind: Triage
target:
  url: https://github.com/o/repo/issues/5
source:
  sandbox: fix-repo-5
  task: recipe-triage-20261003-120000-0001
spec:
  labels:
    - bug
  assessment: A crash on start.
`

// fakeFactory is a factory binary that logs its arguments to a file and
// runs script, a shell case body over "$1".
func fakeFactory(t *testing.T, script string) (bin, argsLog string) {
	t.Helper()
	dir := t.TempDir()
	bin, argsLog = dir+"/factory", dir+"/args"
	body := "#!/bin/sh\necho \"$@\" >> " + argsLog + "\ncase \"$1\" in\n" + script + "\nesac\n"
	if err := os.WriteFile(bin, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, argsLog
}

func startTriage(t *testing.T, bin string) Result {
	t.Helper()
	r := &Runner{Binary: bin, Prober: &fakeProber{probe: TaskProbe{State: ProbeNone}},
		running: map[string]struct{}{}, results: map[string]Result{}}
	r.StartTriage("alice/triage-repo-5", TriageOptions{
		Namespace: "alice", SandboxName: "fix-repo-5", IssueURL: "https://github.com/o/repo/issues/5", RunName: "request/uid-1",
	})
	return waitResult(t, r, "alice/triage-repo-5")
}

func readArgs(t *testing.T, argsLog string) []string {
	t.Helper()
	data, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// The triage runs as a recipe and its result is read back by run name,
// as the triage block drafts are kept in.
func TestStartTriageRunsTheRecipe(t *testing.T) {
	bin, argsLog := fakeFactory(t, `recipe) echo "Running recipe triage..." ;;
sandbox) echo "Waiting for sandbox pod fix-repo-5 to become ready..." >&2; cat <<'DOC'
`+triageTaskOutput+`DOC
;;
*) exit 9 ;;`)
	res := startTriage(t, bin)
	if res.Err != nil {
		t.Fatalf("run: %v\n%s", res.Err, res.Output)
	}
	if got, want := ExtractTriageYAML(res.Output), "triage:\n  labels:\n    - bug\n  assessment: A crash on start."; got != want {
		t.Errorf("draft = %q, want %q", got, want)
	}
	if doc := TriageTaskOutput(res.Output); !strings.HasPrefix(doc, "apiVersion: ") || strings.Contains(doc, "spec:") {
		t.Errorf("kept document = %q, want it without its spec", doc)
	}
	args := readArgs(t, argsLog)
	if len(args) != 2 {
		t.Fatalf("commands run = %q, want recipe then sandbox task output", args)
	}
	if !strings.HasPrefix(args[0], "recipe triage --run-name request/uid-1 ") || !strings.Contains(args[0], "--abort-on-cancel=false") {
		t.Errorf("recipe args = %q", args[0])
	}
	if !strings.HasPrefix(args[1], "sandbox task output fix-repo-5 --namespace alice --run-name request/uid-1") {
		t.Errorf("output args = %q", args[1])
	}
}

// A recipe that fails is a failed triage; there is no result to read.
func TestStartTriageRecipeFailure(t *testing.T) {
	bin, argsLog := fakeFactory(t, `recipe) echo "sandbox fix-repo-5 is busy"; exit 1 ;;
*) exit 9 ;;`)
	if res := startTriage(t, bin); res.Err == nil {
		t.Fatalf("want the recipe's failure, got %q", res.Output)
	}
	if args := readArgs(t, argsLog); len(args) != 1 {
		t.Errorf("commands run = %q, want only the recipe", args)
	}
}

// A Triage task output, whole or behind lines that are not YAML, becomes
// the triage: block; any other draft is left as it is.
func TestNormalizeTriageDraft(t *testing.T) {
	block := "triage:\n  labels:\n    - bug\n  assessment: A crash on start."
	for name, tc := range map[string]struct{ in, want string }{
		"task output":     {triageTaskOutput, block},
		"behind progress": {"Waiting for sandbox pod fix-repo-5 to become ready...\nRunning inside Kubernetes cluster.\n" + triageTaskOutput, block},
		"triage block":    {block, block},
		"other kind":      {strings.Replace(triageTaskOutput, "kind: Triage", "kind: Plan", 1), strings.Replace(triageTaskOutput, "kind: Triage", "kind: Plan", 1)},
		"not yaml":        {"triage: [", "triage: ["},
	} {
		if got := NormalizeTriageDraft(tc.in); got != tc.want {
			t.Errorf("%s: got %q, want %q", name, got, tc.want)
		}
	}
}
