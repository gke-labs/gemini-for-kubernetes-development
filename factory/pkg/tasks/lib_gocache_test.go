package tasks

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/sandbox"
)

// A warm snapshot's caches are filled by a recipe, which runs with the
// sandbox env; classic tasks get their Go env from lib.sh. Unless both name
// the same paths, a restored disk's caches go unused.
func TestLibGoCachesMatchSandboxEnv(t *testing.T) {
	lib, err := scriptsFS.ReadFile("lib.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`export GOCACHE="` + sandbox.GoCachePath + `"`,
		`export GOMODCACHE="` + sandbox.GoModCachePath + `"`,
	} {
		if !strings.Contains(string(lib), want) {
			t.Errorf("lib.sh lacks %s", want)
		}
	}
}

// Off a sandbox (no /workspaces) the caches stay under HOME.
func TestLibGoCachesOffSandbox(t *testing.T) {
	if _, err := os.Stat("/workspaces"); err == nil {
		t.Skip("/workspaces exists here")
	}
	lib, err := scriptsFS.ReadFile("lib.sh")
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	libPath := filepath.Join(home, "lib.sh")
	if err := os.WriteFile(libPath, lib, 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", `source "$1" >/dev/null 2>&1; echo "$GOCACHE|$GOMODCACHE|$GOPATH"`, "_", libPath)
	cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	want := home + "/.cache/go-build|" + home + "/go/pkg/mod|" + home + "/go"
	if got := strings.TrimSpace(string(out)); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
