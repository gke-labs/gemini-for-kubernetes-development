package recipe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/acpd"
)

// oneTask is a task host with a single running task.
type oneTask struct{ dir, token string }

func (o oneTask) Dir(task string) (string, error) {
	if task != filepath.Base(o.dir) {
		return "", os.ErrNotExist
	}
	return o.dir, nil
}
func (o oneTask) Running(string) bool { return true }
func (o oneTask) Token(string) string { return o.token }

func TestDaemonSessionAsksTurnByTurnAsTheTask(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 required")
	}
	script := filepath.Join(t.TempDir(), "agent.py")
	if err := os.WriteFile(script, []byte(fakeACPAgent), 0o600); err != nil {
		t.Fatal(err)
	}
	acpd.Engines["recipe-fake"] = acpd.Engine{Command: "python3", Args: []string{script}, APIKeyEnv: "FAKE_KEY", ModelFlag: "--model"}
	t.Cleanup(func() { delete(acpd.Engines, "recipe-fake") })

	root := t.TempDir()
	taskDir := filepath.Join(root, "plan-20261004-120000")
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sessions := acpd.NewServer(filepath.Join(root, "acpd"), root)
	sessions.SetTasks(oneTask{dir: taskDir, token: "tok"})
	ts := httptest.NewServer(http.StripPrefix("/v1", sessions.Handler()))
	t.Cleanup(func() {
		ts.Close()
		sessions.Close()
	})
	base := ts.URL + "/v1"
	ctx := context.Background()

	if _, err := StartDaemonSession(ctx, base, "wrong", "recipe-fake", "m-1", "k", root, taskDir); err == nil {
		t.Fatal("a wrong token started the task's session")
	}
	s, err := StartDaemonSession(ctx, base, "tok", "recipe-fake", "m-1", "k", root, taskDir)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{
		"turn 1: hello argv=--model m-1 gh=none",
		"turn 2: again argv=--model m-1 gh=none",
	} {
		got, err := s.Ask(ctx, []string{"hello", "again"}[i])
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("ask %d = %q\nwant      %q", i+1, got, want)
		}
	}
	if _, err := s.Ask(ctx, "refuse"); err == nil || !strings.Contains(err.Error(), "refusal") {
		t.Errorf("a refused turn returned %v, want its stop reason", err)
	}
	if _, err := os.Stat(filepath.Join(taskDir, "session", "stream.ndjson")); err != nil {
		t.Errorf("the transcript is not in the task's directory: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(base + "/sessions/" + filepath.Base(taskDir))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("the session outlived Close: %d", resp.StatusCode)
	}
	if err := s.Close(); err != nil {
		t.Errorf("closing a session already gone: %v", err)
	}
}

func TestSandboxRunDropsTheTaskToken(t *testing.T) {
	e := &SandboxExecutor{TaskDir: t.TempDir(), RepoDir: t.TempDir(), Env: []string{
		"PATH=" + os.Getenv("PATH"), "FACTORY_TASK_TOKEN=secret",
	}}
	var out strings.Builder
	if _, err := e.Run(context.Background(), `echo "tok=${FACTORY_TASK_TOKEN:-none}"`, nil, &out); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out.String()); got != "tok=none" {
		t.Errorf("got %q", got)
	}
}
