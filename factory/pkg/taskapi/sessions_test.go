package taskapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/acpd"
)

// sessionAgent is just enough of an ACP agent for a session to start and
// take a turn.
const sessionAgent = `
import json, sys
def send(o):
    sys.stdout.write(json.dumps(o) + "\n"); sys.stdout.flush()
for line in sys.stdin:
    if not line.strip():
        continue
    m = json.loads(line)
    method, mid = m.get("method"), m.get("id")
    if method == "initialize":
        send({"jsonrpc": "2.0", "id": mid, "result": {"protocolVersion": 1}})
    elif method == "session/new":
        send({"jsonrpc": "2.0", "id": mid, "result": {"sessionId": "a"}})
    elif method == "session/prompt":
        send({"jsonrpc": "2.0", "id": mid, "result": {"stopReason": "end_turn"}})
    elif mid is not None:
        send({"jsonrpc": "2.0", "id": mid, "error": {"code": -32601, "message": method}})
`

type sessionsFixture struct {
	url   string
	hc    *http.Client
	c     *Client
	tasks string
	cwd   string
}

func newSessionsServer(t *testing.T) *sessionsFixture {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	script := filepath.Join(t.TempDir(), "agent.py")
	if err := os.WriteFile(script, []byte(sessionAgent), 0o600); err != nil {
		t.Fatal(err)
	}
	acpd.Engines["task-fake"] = acpd.Engine{Command: "python3", Args: []string{script}, APIKeyEnv: "FAKE_KEY"}
	t.Cleanup(func() { delete(acpd.Engines, "task-fake") })

	root := t.TempDir()
	tasks := filepath.Join(root, "tasks")
	tokens := NewTokens()
	s := NewServer(filepath.Join(root, "incoming"), tasks, tokens.Wrap(shellLauncher))
	s.poll = 20 * time.Millisecond
	sessions := acpd.NewServer(filepath.Join(root, "acpd"), root)
	s.HostSessions(sessions, tokens)
	hs := httptest.NewServer(s.Handler())
	t.Cleanup(func() {
		hs.Close()
		sessions.Close()
	})
	return &sessionsFixture{url: hs.URL, hc: hs.Client(), c: NewClient(hs.URL, hs.Client()), tasks: tasks, cwd: t.TempDir()}
}

// call makes a request as the task when token is set, as anybody else
// when it is not, and returns the status and body.
func (f *sessionsFixture) call(t *testing.T, method, path, token, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, f.url+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(acpd.APIKeyHeader, "k")
	if token != "" {
		req.Header.Set(acpd.TaskTokenHeader, token)
	}
	resp, err := f.hc.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(data)
}

func (f *sessionsFixture) createBody(task string) string {
	return fmt.Sprintf(`{"id":%q,"task":%q,"engine":"task-fake","cwd":%q}`, task, task, f.cwd)
}

// startTask posts a task running cmd and returns its token, which the
// task writes to a file so the test can act as it.
func (f *sessionsFixture) startTask(t *testing.T, id, cmd string) string {
	t.Helper()
	return f.startRevise(t, id, "", cmd)
}

// startRevise is startTask for a task continuing session's conversation.
func (f *sessionsFixture) startRevise(t *testing.T, id, session, cmd string) string {
	t.Helper()
	req := post(id, "", `printf %s "$`+EnvTaskToken+`" > token; `+cmd)
	req.Task.Session = session
	if _, err := f.c.Post(context.Background(), req); err != nil {
		t.Fatalf("Post: %v", err)
	}
	path := filepath.Join(f.tasks, id, "token")
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
			return string(data)
		}
	}
	t.Fatal("the task never got a token")
	return ""
}

func TestVersionReportsSessions(t *testing.T) {
	f := newSessionsServer(t)
	_, body := f.call(t, http.MethodGet, "/v1/version", "", "")
	var v Version
	if err := json.Unmarshal([]byte(body), &v); err != nil || v.API != APIVersion || v.Sessions != SessionsVersion {
		t.Errorf("version = %s, %v", body, err)
	}

	c, _ := newTestServer(t)
	resp, err := c.http.Get(c.base + "/v1/version")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var bare Version
	if err := json.NewDecoder(resp.Body).Decode(&bare); err != nil || bare.Sessions != 0 {
		t.Errorf("a server hosting no sessions reports %+v, %v", bare, err)
	}
}

func TestARunningTaskDrivesItsSessionAndEverybodyElseWatches(t *testing.T) {
	f := newSessionsServer(t)
	const task = "plan-20261004-120000"
	token := f.startTask(t, task, "sleep 30")

	if code, body := f.call(t, http.MethodPost, "/v1/sessions", "", f.createBody(task)); code != http.StatusConflict {
		t.Fatalf("anybody created a running task's session: %d %s", code, body)
	}
	if code, body := f.call(t, http.MethodPost, "/v1/sessions", "wrong", f.createBody(task)); code != http.StatusConflict {
		t.Fatalf("a wrong token created a running task's session: %d %s", code, body)
	}
	code, body := f.call(t, http.MethodPost, "/v1/sessions", token, f.createBody(task))
	if code != http.StatusCreated || !strings.Contains(body, `"held":true`) {
		t.Fatalf("the task could not create its session: %d %s", code, body)
	}
	if _, err := os.Stat(filepath.Join(f.tasks, task, "session", "stream.ndjson")); err != nil {
		t.Errorf("the session is not kept in the task's directory: %v", err)
	}

	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, "/v1/sessions/" + task + "/prompt", `{"text":"hi"}`},
		{http.MethodPost, "/v1/sessions/" + task + "/cancel", ""},
		{http.MethodPost, "/v1/sessions/" + task + "/mode", `{"mode":"yolo"}`},
		{http.MethodPost, "/v1/sessions/" + task + "/permission", `{"requestId":"perm-1","optionId":"x"}`},
		{http.MethodDelete, "/v1/sessions/" + task, ""},
	} {
		if code, body := f.call(t, c.method, c.path, "", c.body); code != http.StatusConflict {
			t.Errorf("%s %s by anybody: %d %s, want 409", c.method, c.path, code, body)
		}
	}
	for _, path := range []string{"/v1/sessions/" + task, "/v1/sessions/" + task + "/events?follow=false", "/v1/sessions"} {
		if code, body := f.call(t, http.MethodGet, path, "", ""); code != http.StatusOK {
			t.Errorf("watching %s: %d %s", path, code, body)
		}
	}
	if code, body := f.call(t, http.MethodPost, "/v1/sessions/"+task+"/prompt", token, `{"text":"hi"}`); code != http.StatusAccepted {
		t.Errorf("the task could not prompt its session: %d %s", code, body)
	}

	// Cancelling the task ends its agent.
	if _, err := f.c.Cancel(context.Background(), task, false); err != nil {
		t.Fatal(err)
	}
	if code, _ := f.call(t, http.MethodGet, "/v1/sessions/"+task, "", ""); code != http.StatusNotFound {
		t.Errorf("the cancelled task's session is still there: %d", code)
	}
}

func (f *sessionsFixture) awaitEnd(t *testing.T, task string) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if e, err := f.c.Get(context.Background(), task); err == nil && e.ExitCode != "" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the task never ended")
		}
	}
}

func TestAnEndedTasksSessionIsAnybodys(t *testing.T) {
	f := newSessionsServer(t)
	const task = "triage-20261004-120000"
	f.startTask(t, task, "exit 0")
	f.awaitEnd(t, task)

	code, body := f.call(t, http.MethodPost, "/v1/sessions", "", f.createBody(task))
	if code != http.StatusCreated || strings.Contains(body, `"held":true`) {
		t.Fatalf("continuing an ended task's session: %d %s", code, body)
	}
	if code, body := f.call(t, http.MethodPost, "/v1/sessions/"+task+"/prompt", "", `{"text":"hi"}`); code != http.StatusAccepted {
		t.Errorf("prompting an ended task's session: %d %s", code, body)
	}
}

func TestATaskSessionNeedsTheTask(t *testing.T) {
	f := newSessionsServer(t)
	if code, body := f.call(t, http.MethodPost, "/v1/sessions", "", f.createBody("no-such-task")); code != http.StatusNotFound {
		t.Errorf("a session for no task: %d %s", code, body)
	}
	f.startTask(t, "plan-1", "exit 0")
	body := fmt.Sprintf(`{"id":"other","task":"plan-1","engine":"task-fake","cwd":%q}`, f.cwd)
	if code, resp := f.call(t, http.MethodPost, "/v1/sessions", "", body); code != http.StatusBadRequest {
		t.Errorf("a task's session named otherwise: %d %s", code, resp)
	}
}

// A research session belongs to no task, and is anybody's as before.
func TestASessionWithoutATaskIsUnchanged(t *testing.T) {
	f := newSessionsServer(t)
	body := fmt.Sprintf(`{"id":"research","engine":"task-fake","cwd":%q}`, f.cwd)
	if code, resp := f.call(t, http.MethodPost, "/v1/sessions", "", body); code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, resp)
	}
	if code, resp := f.call(t, http.MethodPost, "/v1/sessions/research/prompt", "", `{"text":"hi"}`); code != http.StatusAccepted {
		t.Errorf("prompt: %d %s", code, resp)
	}
}

// While a revise runs, the session it continues is the revise's: the
// member watches, as they do a running task's, and has it back after.
func TestARunningReviseDrivesTheSessionItContinues(t *testing.T) {
	f := newSessionsServer(t)
	const task, revise = "plan-20261004-120000", "plan-20261004-130000"
	f.startTask(t, task, "exit 0")
	f.awaitEnd(t, task)
	token := f.startRevise(t, revise, task, "sleep 30")

	if code, body := f.call(t, http.MethodPost, "/v1/sessions", "", f.createBody(task)); code != http.StatusConflict {
		t.Fatalf("anybody started a session a revise is running in: %d %s", code, body)
	}
	code, body := f.call(t, http.MethodPost, "/v1/sessions", token, f.createBody(task))
	if code != http.StatusCreated || !strings.Contains(body, `"held":true`) {
		t.Fatalf("the revise could not start the session: %d %s", code, body)
	}
	if _, err := os.Stat(filepath.Join(f.tasks, task, "session", "stream.ndjson")); err != nil {
		t.Errorf("the conversation is not the started task's: %v", err)
	}
	if code, body := f.call(t, http.MethodPost, "/v1/sessions/"+task+"/prompt", "", `{"text":"hi"}`); code != http.StatusConflict {
		t.Errorf("anybody prompted during the revise: %d %s", code, body)
	}
	if code, body := f.call(t, http.MethodPost, "/v1/sessions/"+task+"/prompt", token, `{"text":"rewrite"}`); code != http.StatusAccepted {
		t.Errorf("the revise could not prompt: %d %s", code, body)
	}

	// Cancelling the revise leaves the conversation to the member, once
	// the revise has exited: the cancel only signals it.
	if _, err := f.c.Cancel(context.Background(), revise, false); err != nil {
		t.Fatal(err)
	}
	f.awaitEnd(t, revise)
	code, body = f.call(t, http.MethodGet, "/v1/sessions/"+task, "", "")
	if code != http.StatusOK || strings.Contains(body, `"held":true`) {
		t.Errorf("after the revise: %d %s", code, body)
	}
}
