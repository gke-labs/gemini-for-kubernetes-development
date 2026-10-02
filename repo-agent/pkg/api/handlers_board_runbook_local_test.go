package api

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// localRunRepo makes <root>/<repo> with a run committed on a local
// research/runs branch, the way a local-only run leaves it, and an
// uncommitted receipt in the worktree that must not be read.
func localRunRepo(t *testing.T) (root string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git required")
	}
	root = t.TempDir()
	repo := filepath.Join(root, "granule")
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(repo, 0755); err != nil {
		t.Fatal(err)
	}
	git("init", "-q", "-b", "main")
	write("README.md", "code\n")
	git("add", ".")
	git("commit", "-q", "-m", "main")
	git("checkout", "-q", "-b", runsBranch)
	dir := runsPath + "/gke-std/"
	write(dir+"runbook.md", "# GKE Standard\n")
	write(dir+"deploy.sh", "#!/bin/sh\n")
	write(dir+"receipt-20261002-2116.md", "\n  PLANNED — nothing executed\nmore\n")
	write(dir+"receipt-20261003-0900.md", "VERIFIED (3 resources)\n")
	write(dir+runTargetFile, "TARGET_PR=42\nTARGET_SHA=0123456789abcdef0123456789abcdef01234567\n")
	write(dir+"logs/deploy.log", "nested files are not listed\n")
	write(runsPath+"/other/runbook.md", "# another run\n")
	git("add", ".")
	git("commit", "-q", "-m", "run(gke-std): plan")
	write(dir+"receipt-20261004-0000.md", "TORN-DOWN\n")
	return root
}

func runLocalRunScript(t *testing.T, root, repo, run string) []localRunFile {
	t.Helper()
	out, err := exec.Command("sh", "-c", localRunScript, "sh", root, repo, run).CombinedOutput()
	if err != nil {
		t.Fatalf("localRunScript: %v\n%s", err, out)
	}
	return parseLocalRunFiles(string(out))
}

func TestLocalRunScriptReadsTheCommittedRun(t *testing.T) {
	root := localRunRepo(t)
	files := runLocalRunScript(t, root, "granule", "gke-std")
	got := map[string]string{}
	for _, f := range files {
		got[f.name] = f.line
	}
	want := map[string]string{
		"runbook.md":               "",
		"deploy.sh":                "",
		"receipt-20261002-2116.md": "PLANNED — nothing executed",
		"receipt-20261003-0900.md": "VERIFIED (3 resources)",
		runTargetFile:              "TARGET_PR=42 TARGET_SHA=0123456789abcdef0123456789abcdef01234567",
	}
	if len(got) != len(want) {
		t.Errorf("files = %v, want %v", got, want)
	}
	for k, v := range want {
		if g, ok := got[k]; !ok || g != v {
			t.Errorf("%s = %q (present %v), want %q", k, g, ok, v)
		}
	}
}

func TestLocalRunScriptPrintsNothingWithoutTheRun(t *testing.T) {
	root := localRunRepo(t)
	for _, c := range []struct{ repo, run string }{
		{"granule", "missing"},
		{"not-cloned", "gke-std"},
	} {
		if files := runLocalRunScript(t, root, c.repo, c.run); len(files) != 0 {
			t.Errorf("%s/%s: files = %v, want none", c.repo, c.run, files)
		}
	}
}

func TestLocalRunFileScriptServesACommittedFile(t *testing.T) {
	root := localRunRepo(t)
	out, err := exec.Command("sh", "-c", localRunFileScript, "sh", root, "granule", "gke-std", "runbook.md").CombinedOutput()
	if err != nil || string(out) != "# GKE Standard\n" {
		t.Errorf("runbook.md = %q, %v", out, err)
	}
	// In the worktree, not on the branch: not a record yet.
	if out, err := exec.Command("sh", "-c", localRunFileScript, "sh", root, "granule", "gke-std", "receipt-20261004-0000.md").CombinedOutput(); err == nil {
		t.Errorf("an uncommitted receipt was served: %q", out)
	}
}

func TestLocalRunRowMatchesTheForkRow(t *testing.T) {
	row := localRunRow("myboard", "gke-std", "runbook-granule-gke-std", []localRunFile{
		{name: "deploy.sh"},
		{name: "receipt-20261002-2116.md", line: "PLANNED — nothing executed"},
		{name: "receipt-20261003-0900.md", line: "VERIFIED (3 resources)"},
		{name: "receipt-20261004-0000.md", line: "PLANNED again"},
		{name: "runbook.md"},
		{name: runTargetFile, line: "TARGET_PR=42 TARGET_SHA=abc"},
	})
	if row["localOnly"] != true || row["sandbox"] != "runbook-granule-gke-std" {
		t.Errorf("row = %v, want localOnly and its sandbox", row)
	}
	runbookURL := "/api/board/myboard/runbook/instance/gke-std/file/runbook.md"
	if rb, _ := row["runbook"].(gin.H); rb == nil || rb["htmlURL"] != runbookURL {
		t.Errorf("runbook = %v, want a link to %s", row["runbook"], runbookURL)
	}
	if row["htmlURL"] != runbookURL {
		t.Errorf("htmlURL = %v, want the runbook", row["htmlURL"])
	}
	latest, _ := row["latestReceipt"].(gin.H)
	if latest == nil || latest["name"] != "receipt-20261004-0000.md" || latest["verdict"] != "PLANNED again" {
		t.Errorf("latestReceipt = %v, want the newest", latest)
	}
	// A re-plan of a live run leaves the deploy before it deciding.
	if row["deployed"] != true {
		t.Errorf("deployed = %v, want true from the VERIFIED receipt", row["deployed"])
	}
	if row["target"] != 42 || row["targetSHA"] != "abc" {
		t.Errorf("target = %v %v, want 42 abc", row["target"], row["targetSHA"])
	}
	if strings.Contains(row["htmlURL"].(string), "github.com") {
		t.Error("a local-only run links to GitHub")
	}
}

func TestLocalRunRowWithoutFilesIsNoRow(t *testing.T) {
	if row := localRunRow("b", "r", "sb", nil); row != nil {
		t.Errorf("row = %v, want nil", row)
	}
}
