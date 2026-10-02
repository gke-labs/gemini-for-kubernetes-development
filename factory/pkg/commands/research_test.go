package commands

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// The marker line is the whole machine-readable contract of `factory
// research start`. A caller finds it by prefix and parses the rest as
// JSON, so the prefix has to be unambiguous and the remainder has to be
// exactly one JSON object on one line.
func TestResearchReadyMarkerLineParses(t *testing.T) {
	info := ResearchSandboxInfo{
		Sandbox:   "rsch-kubernetes-1a2b3c4d",
		Namespace: "barney-s",
		SessionID: "1d9f5c1e-3f4a-4f0e-9c3b-2a1b7d8e6f00",
		Repo:      "kubernetes",
		PodIP:     "10.52.3.14",
		Port:      researchACPDPort,
		CWD:       "/workspaces/kubernetes",
	}
	encoded, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	line := ResearchReadyMarker + string(encoded)

	if strings.Count(line, "\n") != 0 {
		t.Error("the marker line must be a single line")
	}
	_, rest, found := strings.Cut(line, ResearchReadyMarker)
	if !found {
		t.Fatalf("marker not found in %q", line)
	}

	var decoded ResearchSandboxInfo
	if err := json.Unmarshal([]byte(rest), &decoded); err != nil {
		t.Fatalf("decoding the marker payload: %v", err)
	}
	if decoded != info {
		t.Errorf("round trip changed the payload:\n got %+v\nwant %+v", decoded, info)
	}
}

// A caller scanning progress output must not mistake a narration line
// for the result. The marker ends with a space so that a bare mention
// of the constant's stem does not match.
func TestResearchReadyMarkerIsDistinct(t *testing.T) {
	progress := []string{
		"Ensuring research sandbox for foo/bar (session s1)...",
		"Connecting to sandbox rsch-bar-1a2b3c4d via envd...",
		"Preparing checkout of bar...",
	}
	for _, line := range progress {
		if strings.HasPrefix(line, ResearchReadyMarker) {
			t.Errorf("progress line matched the marker: %q", line)
		}
	}
	if !strings.HasSuffix(ResearchReadyMarker, " ") {
		t.Error("marker should end with a separator so the JSON starts cleanly")
	}
}

// owner and repo are taken from a user-supplied URL and end up in a
// filesystem path and a clone URL. Anything that could change the
// meaning of either has to be refused up front.
func TestValidRepoPart(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"kubernetes", true},
		{"repo-agent", true},
		{"repo_agent", true},
		{"repo.v2", true},
		{"Repo123", true},
		{"", false},
		{".", false},
		{"..", false},
		{"repo;rm -rf /", false},
		{"repo name", false},
		{"repo$(id)", false},
		{"repo`id`", false},
		{"repo/../etc", false},
		{"repo\nname", false},
		{"repo'quote", false},
		{`repo"quote`, false},
		{"repo&&touch", false},
		{"repo|tee", false},
		{"repo\x00null", false},
	}
	for _, tc := range cases {
		if got := validRepoPart(tc.in); got != tc.want {
			t.Errorf("validRepoPart(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// save-notes attaches to the sandbox `research start` created; it does
// not make one. Both subcommands therefore have to be addressable the
// same way, since the pair of flags is what names that sandbox.
func TestResearchSubcommandsTakeTheSameAddress(t *testing.T) {
	root := NewResearchCommand(context.Background())
	for _, name := range []string{"start", "save-notes"} {
		var sub *cobra.Command
		for _, c := range root.Commands() {
			if c.Name() == name {
				sub = c
			}
		}
		if sub == nil {
			t.Errorf("factory research has no %q subcommand", name)
			continue
		}
		for _, flag := range []string{"url", "session"} {
			if sub.Flags().Lookup(flag) == nil {
				t.Errorf("%s does not take --%s", name, flag)
			}
		}
	}
}

// Both halves of the URL reach a shell — as a path under /workspaces
// and as part of the clone URL the notes are pushed to — so the parse
// is also the gate.
func TestParseGitHubRepoURL(t *testing.T) {
	cases := []struct {
		in          string
		owner, repo string
		wantErr     bool
	}{
		{in: "https://github.com/gke-labs/repo-agent", owner: "gke-labs", repo: "repo-agent"},
		{in: "https://github.com/gke-labs/repo-agent.git", owner: "gke-labs", repo: "repo-agent"},
		{in: "https://github.com/gke-labs/repo-agent/", owner: "gke-labs", repo: "repo-agent"},
		{in: "https://github.com/gke-labs", wantErr: true},
		{in: "", wantErr: true},
		// A path that walks out of the checkout, and a name that would
		// end the shell word it lands in.
		{in: "https://github.com/../../etc/repo", wantErr: true},
		{in: "https://github.com/owner/repo;id", wantErr: true},
		{in: "https://github.com/owner/$(id)", wantErr: true},
	}
	for _, tc := range cases {
		owner, repo, err := parseGitHubRepoURL(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseGitHubRepoURL(%q) = %q/%q, want an error", tc.in, owner, repo)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseGitHubRepoURL(%q): %v", tc.in, err)
			continue
		}
		if owner != tc.owner || repo != tc.repo {
			t.Errorf("parseGitHubRepoURL(%q) = %q/%q, want %q/%q", tc.in, owner, repo, tc.owner, tc.repo)
		}
	}
}

// The port reported to the caller must be the one acpd actually binds.
// They are separate constants in separate packages, so nothing but a
// test stops them drifting apart.
func TestResearchPortMatchesACPDDefault(t *testing.T) {
	if researchACPDPort != 49984 {
		t.Errorf("researchACPDPort = %d, want acpd's default 49984", researchACPDPort)
	}
}

// runCheckoutScript runs researchCheckoutScript against a stand-in git
// that records its arguments, and fails a clone when gitFails is set.
func runCheckoutScript(t *testing.T, workspaces string, gitFails bool) (stdout, stderr, gitArgs string) {
	t.Helper()
	bin := t.TempDir()
	argsLog := filepath.Join(t.TempDir(), "git-args")
	fakeGit := `#!/bin/sh
printf '%s\n' "$*" >> "$GIT_ARGS_LOG"
for a in "$@"; do
  case "$a" in
    clone)
      if [ -n "$GIT_FAILS" ]; then
        echo "fatal: could not read Username for 'https://github.com': No such device or address" >&2
        exit 128
      fi
      mkdir -p "$REPO_NAME/.git"
      exit 0 ;;
    fetch) exit 0 ;;
  esac
done
`
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(fakeGit), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", researchCheckoutScript)
	cmd.Env = []string{
		"PATH=" + bin + ":/usr/bin:/bin",
		"GIT_ARGS_LOG=" + argsLog,
		"WORKSPACES=" + workspaces,
		"REPO_NAME=granule",
		"CLONE_URL=https://github.com/gke-labs/granule.git",
	}
	if gitFails {
		cmd.Env = append(cmd.Env, "GIT_FAILS=1")
	}
	var out, errOut strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errOut
	_ = cmd.Run() // the outcome is read from the output, as in the sandbox
	logged, _ := os.ReadFile(argsLog)
	return out.String(), errOut.String(), string(logged)
}

// A private repository only clones if git is told where the token is:
// the helper has to reach git on the command line, after an empty one
// that drops anything inherited.
func TestResearchCheckoutAuthenticatesThroughGh(t *testing.T) {
	workspaces := t.TempDir()
	stdout, stderr, gitArgs := runCheckoutScript(t, workspaces, false)
	if err := checkoutOutcome(stdout, stderr); err != nil {
		t.Fatalf("checkoutOutcome: %v", err)
	}
	want := "-c credential.helper= -c credential.helper=!gh auth git-credential clone https://github.com/gke-labs/granule.git"
	if strings.TrimSpace(gitArgs) != want {
		t.Errorf("git called with %q, want %q", strings.TrimSpace(gitArgs), want)
	}
	if _, err := os.Stat(filepath.Join(workspaces, "granule", ".git")); err != nil {
		t.Errorf("clone did not land: %v", err)
	}

	// A second run finds the checkout and fetches, authenticated the same way.
	stdout, stderr, gitArgs = runCheckoutScript(t, workspaces, false)
	if err := checkoutOutcome(stdout, stderr); err != nil {
		t.Fatalf("checkoutOutcome on refetch: %v", err)
	}
	if want := "-c credential.helper= -c credential.helper=!gh auth git-credential fetch origin"; strings.TrimSpace(gitArgs) != want {
		t.Errorf("git called with %q, want %q", strings.TrimSpace(gitArgs), want)
	}
}

// envd reports a command that exited non-zero as a success, so a failed
// clone has to be caught from the output. This is what wrote a ready
// receipt for a sandbox with no checkout in it.
func TestResearchCheckoutFailureIsReported(t *testing.T) {
	stdout, stderr, _ := runCheckoutScript(t, t.TempDir(), true)
	err := checkoutOutcome(stdout, stderr)
	if err == nil {
		t.Fatal("a failed clone was reported as a checkout")
	}
	if !strings.Contains(err.Error(), "could not read Username") {
		t.Errorf("error %q does not carry git's reason", err)
	}
}
