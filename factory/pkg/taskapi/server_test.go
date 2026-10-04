package taskapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/envd"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/spool"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/taskoutput"
)

// shellLauncher runs the task's CMD env var in the task directory as the
// task script would run the recipe, in a process group of its own.
func shellLauncher(taskDir string, env map[string]string) error {
	cmd := exec.Command("sh", "-c", envd.TaskScript(envd.NewTaskFiles(taskDir), "("+env["CMD"]+")"))
	cmd.Dir = taskDir
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

func newTestServer(t *testing.T) (*Client, string) {
	t.Helper()
	root := t.TempDir()
	tasks := filepath.Join(root, "tasks")
	s := NewServer(filepath.Join(root, "incoming"), tasks, shellLauncher)
	s.poll = 20 * time.Millisecond
	s.deadGrace = 100 * time.Millisecond
	hs := httptest.NewServer(s.Handler())
	t.Cleanup(hs.Close)
	return NewClient(hs.URL, hs.Client()), tasks
}

func post(id, runName, cmd string) PostRequest {
	return PostRequest{
		Task:   spool.Task{ID: id, RunName: runName, Recipe: "plan", SubmittedAt: time.Now()},
		Recipe: []byte("name: plan\n"),
		Inputs: map[string]string{"issue": "1"},
		Env:    map[string]string{"CMD": cmd, "SECRET": "s3cret"},
	}
}

func readLog(t *testing.T, c *Client, id string, offset int64) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rc, err := c.Log(ctx, id, offset, true)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("reading log: %v", err)
	}
	return string(data)
}

func TestVersion(t *testing.T) {
	c, _ := newTestServer(t)
	v, err := c.Version(context.Background())
	if err != nil || v != APIVersion {
		t.Fatalf("Version = %d, %v", v, err)
	}
}

func TestPostFollowAndRead(t *testing.T) {
	c, tasks := newTestServer(t)
	ctx := context.Background()
	resp, err := c.Post(ctx, post("plan-20261004-120000", "run-1", `echo one; sleep 0.3; echo two; echo '{}' > task-output.yaml; exit 3`))
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	if resp.Existed || resp.Task.ID != "plan-20261004-120000" || resp.Task.RunName != "run-1" {
		t.Fatalf("Post = %+v", resp)
	}
	if resp.Task.State != spool.Running && resp.Task.State != spool.Exited {
		t.Fatalf("not started: %+v", resp.Task)
	}
	if got := readLog(t, c, resp.Task.ID, 0); got != "one\ntwo\n" {
		t.Fatalf("log = %q", got)
	}
	if got := readLog(t, c, resp.Task.ID, 4); got != "two\n" {
		t.Fatalf("log from 4 = %q", got)
	}
	e, err := c.Get(ctx, resp.Task.ID)
	if err != nil || e.ExitCode != "3" {
		t.Fatalf("Get = %+v, %v", e, err)
	}

	// The environment reached the process and was never written down.
	dir := filepath.Join(tasks, resp.Task.ID)
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if data, _ := os.ReadFile(path); strings.Contains(string(data), "s3cret") {
			t.Errorf("%s holds the environment", path)
		}
		return nil
	})
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("task directory mode = %v, %v", info.Mode(), err)
	}

	recipe, err := c.ReadFile(ctx, resp.Task.ID, spool.RecipeFile)
	if err != nil || string(recipe) != "name: plan\n" {
		t.Fatalf("ReadFile recipe = %q, %v", recipe, err)
	}
	if _, err := c.ReadFile(ctx, resp.Task.ID, "nope.json"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ReadFile missing = %v, want ErrNotExist", err)
	}
	names, err := c.Files(ctx, resp.Task.ID)
	if err != nil || !contains(names, spool.TaskFile) || !contains(names, spool.RecipeFile) {
		t.Fatalf("Files = %v, %v", names, err)
	}
}

func TestPostIsIdempotent(t *testing.T) {
	c, _ := newTestServer(t)
	ctx := context.Background()
	if _, err := c.Post(ctx, post("plan-20261004-120001", "run-a", "echo once >> ran")); err != nil {
		t.Fatalf("Post: %v", err)
	}
	again, err := c.Post(ctx, post("plan-20261004-120001", "", "echo twice"))
	if err != nil || !again.Existed {
		t.Fatalf("same id: %+v, %v", again, err)
	}
	byName, err := c.Post(ctx, post("plan-20261004-120002", "run-a", "echo twice"))
	if err != nil || !byName.Existed || byName.Task.ID != "plan-20261004-120001" {
		t.Fatalf("same run name: %+v, %v", byName, err)
	}
	entries, err := c.List(ctx)
	if err != nil || len(entries) != 1 {
		t.Fatalf("List = %+v, %v", entries, err)
	}
}

func TestPostRejectsBadTasks(t *testing.T) {
	c, _ := newTestServer(t)
	ctx := context.Background()
	for _, id := range []string{"", "../x", ".hidden", "a/b"} {
		_, err := c.Post(ctx, post(id, "", "true"))
		var se *StatusError
		if !errors.As(err, &se) || se.Code != http.StatusBadRequest {
			t.Errorf("id %q: %v, want 400", id, err)
		}
	}
	req := post("plan-20261004-120003", "", "true")
	req.Recipe = nil
	if _, err := c.Post(ctx, req); err == nil {
		t.Errorf("a task without a recipe was started")
	}
}

func TestFilesAreGuarded(t *testing.T) {
	c, tasks := newTestServer(t)
	ctx := context.Background()
	resp, err := c.Post(ctx, post("plan-20261004-120004", "", "true"))
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	id := resp.Task.ID
	// A task the spool claimed has its environment on disk.
	if err := os.WriteFile(filepath.Join(tasks, id, spool.EnvFile), []byte(`{"SECRET":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var se *StatusError
	if _, err := c.ReadFile(ctx, id, spool.EnvFile); !errors.As(err, &se) || se.Code != http.StatusForbidden {
		t.Errorf("reading env.json: %v, want 403", err)
	}
	if names, _ := c.Files(ctx, id); contains(names, spool.EnvFile) {
		t.Errorf("Files lists env.json: %v", names)
	}
	if err := c.WriteFile(ctx, id, spool.TaskFile, []byte("{}")); !errors.As(err, &se) || se.Code != http.StatusForbidden {
		t.Errorf("writing task.json: %v, want 403", err)
	}
	if err := c.WriteFile(ctx, id, taskoutput.AppliedFile, []byte("ok")); err != nil {
		t.Errorf("writing the applied mark: %v", err)
	}
	if data, err := c.ReadFile(ctx, id, taskoutput.AppliedFile); err != nil || string(data) != "ok" {
		t.Errorf("applied mark = %q, %v", data, err)
	}
	if _, err := c.Get(ctx, "no-such-task"); !errors.As(err, &se) || se.Code != http.StatusNotFound {
		t.Errorf("Get missing: %v, want 404", err)
	}
	if _, err := c.ReadFile(ctx, "no-such-task", spool.TaskFile); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("ReadFile in a missing task: %v, want ErrNotExist", err)
	}
}

func TestCancelEndsTheGroup(t *testing.T) {
	for _, tc := range []struct {
		kill bool
		code string
	}{{false, "143"}, {true, "137"}} {
		c, tasks := newTestServer(t)
		ctx := context.Background()
		// The child outlives a kill of the task's first process alone.
		resp, err := c.Post(ctx, post("plan-20261004-120005", "", `sh -c 'sleep 30; echo leaked > leaked' & echo started; wait`))
		if err != nil {
			t.Fatalf("Post: %v", err)
		}
		e, err := c.Cancel(ctx, resp.Task.ID, tc.kill)
		if err != nil || e.ExitCode != tc.code {
			t.Fatalf("Cancel(kill=%v) = %+v, %v; want exit %s", tc.kill, e, err, tc.code)
		}
		readLog(t, c, resp.Task.ID, 0) // ends, the task having ended
		pid, err := readPID(envd.NewTaskFiles(filepath.Join(tasks, resp.Task.ID)))
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for syscall.Kill(-pid, 0) == nil && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if syscall.Kill(-pid, 0) == nil {
			t.Errorf("kill=%v: the task's process group is still there", tc.kill)
		}
	}
}

func TestLogGivesADeadTask137(t *testing.T) {
	c, tasks := newTestServer(t)
	id := "plan-20261004-120006"
	dir := filepath.Join(tasks, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// A task whose process went without writing its exit code: a pid
	// that is not running.
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	tf := envd.NewTaskFiles(dir)
	if err := os.WriteFile(tf.PIDFile, []byte(strconv.Itoa(cmd.Process.Pid)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tf.LogFile, []byte("partial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readLog(t, c, id, 0); got != "partial\n" {
		t.Fatalf("log = %q", got)
	}
	if code, _ := os.ReadFile(tf.ExitCodeFile); strings.TrimSpace(string(code)) != "137" {
		t.Fatalf("exit code = %q, want 137", code)
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
