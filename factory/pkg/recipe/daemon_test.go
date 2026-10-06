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

func TestARevisePromptsTheSessionItRevises(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 required")
	}
	script := filepath.Join(t.TempDir(), "agent.py")
	if err := os.WriteFile(script, []byte(fakeACPAgent), 0o600); err != nil {
		t.Fatal(err)
	}
	acpd.Engines["recipe-fake"] = acpd.Engine{Command: "python3", Args: []string{script}, APIKeyEnv: "FAKE_KEY"}
	t.Cleanup(func() { delete(acpd.Engines, "recipe-fake") })

	root := t.TempDir()
	taskDir := filepath.Join(root, "plan-20261004-120000")
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		t.Fatal(err)
	}
	session := filepath.Base(taskDir)
	sessions := acpd.NewServer(filepath.Join(root, "acpd"), root)
	// The daemon holds the started task's session for the revise: its
	// token is the revise's.
	sessions.SetTasks(oneTask{dir: taskDir, token: "revise-tok"})
	ts := httptest.NewServer(http.StripPrefix("/v1", sessions.Handler()))
	t.Cleanup(func() {
		ts.Close()
		sessions.Close()
	})
	base := ts.URL + "/v1"
	ctx := context.Background()
	live := func() bool {
		resp, err := http.Get(base + "/sessions/" + session)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}

	// Nobody has it open: the revise starts it, and ends it. Nothing asked
	// in it before (the first revise), so there is no conversation to load: it is fresh.
	s, err := OpenDaemonSession(ctx, base, "revise-tok", session, "recipe-fake", "", "k", root)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Fresh() {
		t.Error("a session with no conversation to load is not fresh")
	}
	if got, err := s.Ask(ctx, "rewrite"); err != nil || !strings.Contains(got, "turn 1: rewrite") {
		t.Errorf("ask = %q, %v", got, err)
	}
	if err := s.Close(); err != nil || live() {
		t.Errorf("a session the revise started outlived it: %v", err)
	}

	// A member has it open: the revise uses it and leaves it open.
	member, err := StartDaemonSession(ctx, base, "revise-tok", "recipe-fake", "", "k", root, taskDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = member.Close() })
	s, err = OpenDaemonSession(ctx, base, "revise-tok", session, "recipe-fake", "", "k", root)
	if err != nil {
		t.Fatal(err)
	}
	if s.Fresh() {
		t.Error("a session the member has open is fresh")
	}
	if _, err := s.Ask(ctx, "rewrite"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil || !live() {
		t.Errorf("the revise closed the member's session: %v", err)
	}
}

func TestARevisePromptsNothingIntoABusySession(t *testing.T) {
	var prompted bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"id":"plan-1","busy":true}`))
			return
		}
		prompted = true
		w.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(ts.Close)
	_, err := OpenDaemonSession(context.Background(), ts.URL+"/v1", "tok", "plan-1", "gemini", "", "k", "/r")
	if err == nil || !strings.Contains(err.Error(), "busy") {
		t.Errorf("err = %v, want busy", err)
	}
	if prompted {
		t.Error("the revise sent something to a busy session")
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
