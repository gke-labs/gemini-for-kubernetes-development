package taskapi

import (
	"archive/tar"
	"bytes"
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

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/spool"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/taskfiles"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/taskoutput"
)

// shellLauncher runs the task's CMD env var in the task directory as the
// daemon runs a recipe.
func shellLauncher(taskDir string, env map[string]string) error {
	return spool.Start(context.Background(), taskDir, "("+env["CMD"]+")", taskDir, env)
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
	if err != nil || v.API != APIVersion || v.Secrets != SecretsVersion {
		t.Fatalf("Version = %+v, %v", v, err)
	}
}

// Secrets reach the task directory for the runner, and nobody else: not
// the task's environment, not the file API.
func TestPostSecrets(t *testing.T) {
	c, tasks := newTestServer(t)
	ctx := context.Background()
	req := post("research-20261005-120000", "", `env > env.txt; cat secrets.json > seen.txt`)
	req.Secrets = map[string]string{"GITHUB_TOKEN": "tok3n"}
	resp, err := c.Post(ctx, req)
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	readLog(t, c, resp.Task.ID, 0)
	dir := filepath.Join(tasks, resp.Task.ID)
	if env, _ := os.ReadFile(filepath.Join(dir, "env.txt")); strings.Contains(string(env), "tok3n") || !strings.Contains(string(env), "SECRET=s3cret") {
		t.Errorf("the task's environment:\n%s", env)
	}
	if seen, _ := os.ReadFile(filepath.Join(dir, "seen.txt")); string(seen) != `{"GITHUB_TOKEN":"tok3n"}` {
		t.Errorf("the task read %q from %s", seen, spool.SecretsFile)
	}
	if info, err := os.Stat(filepath.Join(dir, spool.SecretsFile)); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("%s mode = %v, %v", spool.SecretsFile, info, err)
	}
	if _, err := c.ReadFile(ctx, resp.Task.ID, spool.SecretsFile); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Errorf("ReadFile %s = %v, want refused", spool.SecretsFile, err)
	}
	if names, err := c.Files(ctx, resp.Task.ID); err != nil || contains(names, spool.SecretsFile) {
		t.Errorf("Files = %v, %v", names, err)
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
	if err != nil || e.ExitCode != "3" || e.Ended.IsZero() {
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
	if err := c.WriteFile(ctx, id, taskoutput.ApplyFailedFile, []byte(`{"error":"x"}`)); err != nil {
		t.Errorf("writing the apply-failed mark: %v", err)
	}
	if err := c.WriteFile(ctx, id, taskoutput.ApplyFailedFile, nil); err != nil {
		t.Errorf("emptying the apply-failed mark: %v", err)
	}
	if _, err := c.Get(ctx, "no-such-task"); !errors.As(err, &se) || se.Code != http.StatusNotFound {
		t.Errorf("Get missing: %v, want 404", err)
	}
	if _, err := c.ReadFile(ctx, "no-such-task", spool.TaskFile); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("ReadFile in a missing task: %v, want ErrNotExist", err)
	}
}

// A result the agent writes after its task, in a later turn of the
// conversation, is what the task's task output says when it is next read
// or listed.
func TestTaskOutputFollowsTheResultFile(t *testing.T) {
	c, tasks := newTestServer(t)
	ctx := context.Background()
	req := post("research-20261013-120000", "", "true")
	req.Task.Recipe = "research"
	req.Task.Output = &taskoutput.Decl{Kind: "Notes", From: "notes.md", Optional: true}
	resp, err := c.Post(ctx, req)
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	id := resp.Task.ID
	frame := &taskoutput.Document{APIVersion: taskoutput.APIVersion, Kind: "Notes", Target: taskoutput.Target{URL: "https://github.com/o/r"}, Source: taskoutput.Source{Task: id}}
	if err := taskoutput.WriteFrame(filepath.Join(tasks, id), frame); err != nil {
		t.Fatal(err)
	}
	if names, _ := c.Files(ctx, id); contains(names, taskoutput.File) {
		t.Fatalf("a task output listed before the notes were written: %v", names)
	}
	if err := os.WriteFile(filepath.Join(tasks, id, "notes.md"), []byte("# Notes\nFound it.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if names, _ := c.Files(ctx, id); !contains(names, taskoutput.File) {
		t.Errorf("the notes' task output is not listed: %v", names)
	}
	data, err := c.ReadFile(ctx, id, taskoutput.File)
	if err != nil || !strings.Contains(string(data), "Found it.") {
		t.Errorf("task output = %s, %v; want the notes", data, err)
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
		pid, err := readPID(taskfiles.NewTaskFiles(filepath.Join(tasks, resp.Task.ID)))
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
	tf := taskfiles.NewTaskFiles(dir)
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

// A task keeps the recipe it was posted with, whole, beside the copy the
// sandbox runs; a caller older than the source keeps none.
func TestPostKeepsTheRecipeSource(t *testing.T) {
	c, _ := newTestServer(t)
	ctx := context.Background()
	req := post("plan-20261014-090000", "", "true")
	req.Source = []byte("name: plan\non: [issue]\ntask-output: {kind: Plan, from: plan.md}\n")
	if _, err := c.Post(ctx, req); err != nil {
		t.Fatal(err)
	}
	if src, err := c.ReadFile(ctx, req.Task.ID, spool.RecipeSourceFile); err != nil || string(src) != string(req.Source) {
		t.Errorf("source = %q, %v", src, err)
	}
	old := post("plan-20261014-090001", "", "true")
	if _, err := c.Post(ctx, old); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ReadFile(ctx, old.Task.ID, spool.RecipeSourceFile); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a task posted without a source has one: %v", err)
	}
}

// A woken sandbox's task server refuses its port until factory daemon
// listens: the connect asks again, over a new forward each time, until it
// answers; one that never does fails with its last error once the time is
// up.
func TestAwaitServerAsksAgainUntilItAnswers(t *testing.T) {
	ctx := context.Background()
	calls, reopens := 0, 0
	v, err := awaitServer(ctx, time.Second, time.Millisecond,
		func(context.Context) (Version, error) {
			calls++
			if calls < 4 {
				return Version{}, errors.New("connection refused")
			}
			return Version{API: APIVersion}, nil
		},
		func(context.Context) error { reopens++; return nil })
	if err != nil || v.API != APIVersion || calls != 4 || reopens != 3 {
		t.Fatalf("version %+v, err %v, calls %d, reopens %d", v, err, calls, reopens)
	}

	_, err = awaitServer(ctx, 20*time.Millisecond, 5*time.Millisecond,
		func(context.Context) (Version, error) { return Version{}, errors.New("EOF") },
		func(context.Context) error { return nil })
	if err == nil || err.Error() != "EOF" {
		t.Fatalf("err = %v, want the last error", err)
	}
}

// A task's session files are read by their relative name; nothing above
// the session directory is.
func TestReadSessionFile(t *testing.T) {
	c, tasks := newTestServer(t)
	ctx := context.Background()
	resp, err := c.Post(ctx, post("fix-20261020-120000", "", `mkdir -p session && echo '{"seq":1}' > session/stream.ndjson`))
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	readLog(t, c, resp.Task.ID, 0)
	data, err := c.ReadFile(ctx, resp.Task.ID, "session/stream.ndjson")
	if err != nil || string(data) != "{\"seq\":1}\n" {
		t.Fatalf("ReadFile session/stream.ndjson = %q, %v", data, err)
	}
	if _, err := c.ReadFile(ctx, resp.Task.ID, "session/record.json"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing session file: %v, want os.ErrNotExist", err)
	}
	if err := os.WriteFile(filepath.Join(tasks, resp.Task.ID, "outside"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ReadFile(ctx, resp.Task.ID, "session/../outside"); err == nil {
		t.Errorf("session/../outside was served")
	}
}

// AllFiles lists the task's files one directory down too, which ReadFile
// then reads by their relative names; the secrets stay out.
func TestAllFiles(t *testing.T) {
	c, _ := newTestServer(t)
	ctx := context.Background()
	req := post("fix-20261021-120000", "", `mkdir -p session steps .hidden && echo t > session/stream.ndjson && echo s > steps/01-clone.log && echo h > .hidden/x && echo r > result.yaml`)
	req.Secrets = map[string]string{"GITHUB_TOKEN": "tok3n"}
	resp, err := c.Post(ctx, req)
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	readLog(t, c, resp.Task.ID, 0)
	names, err := c.AllFiles(ctx, resp.Task.ID)
	if err != nil {
		t.Fatalf("AllFiles: %v", err)
	}
	for _, want := range []string{"session/stream.ndjson", "steps/01-clone.log", "result.yaml"} {
		if !contains(names, want) {
			t.Errorf("AllFiles = %v, missing %s", names, want)
		}
	}
	for _, not := range []string{spool.SecretsFile, spool.EnvFile, ".hidden/x"} {
		if contains(names, not) {
			t.Errorf("AllFiles = %v, lists %s", names, not)
		}
	}
	if data, err := c.ReadFile(ctx, resp.Task.ID, "steps/01-clone.log"); err != nil || string(data) != "s\n" {
		t.Errorf("ReadFile steps/01-clone.log = %q, %v", data, err)
	}
	if top, _ := c.Files(ctx, resp.Task.ID); contains(top, "steps/01-clone.log") {
		t.Errorf("Files lists a subdirectory's file: %v", top)
	}
}

// Files uploaded before a task are in its directory when its first step
// runs; a task naming no such upload, or an unsafe place for it, is
// refused and nothing runs.
func TestPostTakesAnUpload(t *testing.T) {
	c, tasks := newTestServer(t)
	ctx := context.Background()
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "prs", "7"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"staged.json": `{"repo":"o/r"}`, "prs/7/feedback.jsonl": "{}\n"} {
		if err := os.WriteFile(filepath.Join(src, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Upload(ctx, "retro-20261028-120000", src); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	req := post("retro-20261028-120000", "", `cat staged/staged.json staged/prs/7/feedback.jsonl > seen.txt`)
	req.Upload, req.UploadTo = "retro-20261028-120000", "staged"
	resp, err := c.Post(ctx, req)
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	readLog(t, c, resp.Task.ID, 0)
	if seen, _ := os.ReadFile(filepath.Join(tasks, resp.Task.ID, "seen.txt")); string(seen) != `{"repo":"o/r"}{}`+"\n" {
		t.Errorf("the task read %q", seen)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(tasks), "uploads", "retro-20261028-120000")); !os.IsNotExist(err) {
		t.Errorf("the upload is left behind: %v", err)
	}

	for name, tc := range map[string]struct{ upload, to string }{
		"no such upload": {"retro-missing", "staged"},
		"outside":        {"retro-20261028-130000", "../escape"},
	} {
		if tc.upload != "retro-missing" {
			if err := c.Upload(ctx, tc.upload, src); err != nil {
				t.Fatal(err)
			}
		}
		req := post(tc.upload, "", `touch ran`)
		req.Upload, req.UploadTo = tc.upload, tc.to
		var se *StatusError
		if _, err := c.Post(ctx, req); !errors.As(err, &se) || se.Code != 400 {
			t.Errorf("%s: %v, want 400", name, err)
		}
		if _, err := os.Stat(filepath.Join(tasks, tc.upload)); !os.IsNotExist(err) {
			t.Errorf("%s: the task directory is left: %v", name, err)
		}
	}
}

// An upload holding anything but directories and regular files, or a
// path out of it, is refused.
func TestUntarRefusesUnsafeEntries(t *testing.T) {
	for name, hdr := range map[string]*tar.Header{
		"parent":  {Name: "../x", Typeflag: tar.TypeReg, Size: 1, Mode: 0o644},
		"symlink": {Name: "l", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"},
		"abs":     {Name: "/etc/x", Typeflag: tar.TypeReg, Size: 1, Mode: 0o644},
	} {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		_ = tw.WriteHeader(hdr)
		if hdr.Size > 0 {
			_, _ = tw.Write([]byte("x"))
		}
		_ = tw.Close()
		if err := untar(t.TempDir(), &buf, 1<<20); err == nil {
			t.Errorf("%s: unpacked", name)
		}
	}
}
