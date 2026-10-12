package commands

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// sandbox exec runs through kube exec now: kubectl's arguments carry the
// env, the cwd and the command as arguments, so none needs quoting.
func TestSandboxExecArgs(t *testing.T) {
	args := sandboxExecArgs("ns", "fix-x-0", "/workspaces/my repo", []string{"A=1", "B=two words", "bogus"}, `echo "$A-$B" && pwd`)
	want := []string{"exec", "-i", "-n", "ns", "fix-x-0", "-c", "sandbox", "--", "env", "A=1", "B=two words", "sh", "-c", `cd "$0" && exec sh -c "$1"`, "/workspaces/my repo", `echo "$A-$B" && pwd`}
	if !slices.Equal(args, want) {
		t.Fatalf("args =\n%q\nwant\n%q", args, want)
	}

	// What runs in the pod, run here: the env and cwd apply, the command
	// is the shell's, and a cwd with a space is fine.
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh required")
	}
	dir := t.TempDir() + "/my repo"
	if err := exec.Command("mkdir", "-p", dir).Run(); err != nil {
		t.Fatal(err)
	}
	tail := sandboxExecArgs("ns", "pod", dir, []string{"A=1", "B=two words"}, `echo "$A-$B" && pwd`)[8:] // from "env"
	out, err := exec.Command(tail[0], tail[1:]...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", sh, err, out)
	}
	if got := strings.TrimSpace(string(out)); !strings.HasPrefix(got, "1-two words\n") || !strings.HasSuffix(got, "/my repo") {
		t.Errorf("ran %q", got)
	}
}
