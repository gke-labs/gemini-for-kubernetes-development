package spool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/taskfiles"
)

// localRemote runs the client side's scripts with this machine's shell,
// with /workspaces moved into a temporary directory.
type localRemote struct{ root string }

func (l localRemote) path(p string) string { return strings.ReplaceAll(p, "/workspaces", l.root) }

func (l localRemote) WriteFile(_ context.Context, dest string, data []byte) error {
	dest = l.path(dest)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dest, data, 0o644)
}

// Exec, like envd's, does not fail on a non-zero exit.
func (l localRemote) Exec(_ context.Context, script, _ string, _ map[string]string, _ io.Reader, stdout, stderr io.Writer) error {
	cmd := exec.Command("sh", "-c", l.path(script))
	cmd.Stdout, cmd.Stderr = stdout, stderr
	_ = cmd.Run()
	return nil
}

func (l localRemote) incoming() string { return l.path(IncomingDir) }
func (l localRemote) tasks() string    { return l.path(taskfiles.DefaultTasksDir) }

// spoolTask leaves a task in incoming as a client does (the task server
// in production): written under a dot-name, the env file private, and
// renamed into place.
func spoolTask(t *testing.T, r localRemote, task Task, recipeYAML []byte, inputs, env map[string]string) {
	t.Helper()
	staging := filepath.Join(r.incoming(), "."+task.ID)
	if err := os.MkdirAll(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, v := range map[string]any{InputsFile: inputs, EnvFile: env, TaskFile: task} {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(staging, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(staging, RecipeFile), recipeYAML, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(staging, filepath.Join(r.incoming(), task.ID)); err != nil {
		t.Fatal(err)
	}
}

func submit(t *testing.T, r localRemote, id, runName string, at time.Time) {
	t.Helper()
	task := Task{ID: id, RunName: runName, Recipe: "triage", SubmittedAt: at}
	spoolTask(t, r, task, []byte("name: triage\n"), map[string]string{"issue_number": "7"}, map[string]string{"GITHUB_TOKEN": "secret"})
}

// listTasks is what the task server lists: ListScript, run here.
func listTasks(t *testing.T, r localRemote) []Entry {
	t.Helper()
	var out strings.Builder
	if err := r.Exec(context.Background(), ListScript(IncomingDir, taskfiles.DefaultTasksDir), "/workspaces", nil, nil, &out, nil); err != nil {
		t.Fatal(err)
	}
	return ParseList(out.String())
}

func TestSubmitAppearsWholeAndPrivate(t *testing.T) {
	r := localRemote{t.TempDir()}
	submit(t, r, "recipe-triage-1", "c1", time.Now())

	dir := filepath.Join(r.incoming(), "recipe-triage-1")
	for _, f := range []string{TaskFile, RecipeFile, InputsFile, EnvFile} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	if fi, err := os.Stat(filepath.Join(dir, EnvFile)); err == nil && fi.Mode().Perm() != 0o600 {
		t.Errorf("%s mode = %v, want 0600: it holds tokens", EnvFile, fi.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(r.incoming(), ".recipe-triage-1")); !os.IsNotExist(err) {
		t.Error("the staging directory is left behind")
	}
}

func TestClaimSkipsStagingAndStartsWithEnv(t *testing.T) {
	r := localRemote{t.TempDir()}
	submit(t, r, "recipe-triage-1", "", time.Now())
	if err := os.MkdirAll(filepath.Join(r.incoming(), ".recipe-triage-2"), 0o755); err != nil {
		t.Fatal(err)
	}

	var launched []string
	var gotEnv map[string]string
	ClaimAll(context.Background(), r.incoming(), r.tasks(), func(taskDir string, env map[string]string) error {
		launched = append(launched, filepath.Base(taskDir))
		gotEnv = env
		return nil
	})

	if len(launched) != 1 || launched[0] != "recipe-triage-1" {
		t.Fatalf("launched %v, want only recipe-triage-1", launched)
	}
	if gotEnv["GITHUB_TOKEN"] != "secret" {
		t.Errorf("env = %v, want the submitted environment", gotEnv)
	}
	if _, err := os.Stat(filepath.Join(r.tasks(), "recipe-triage-1", EnvFile)); !os.IsNotExist(err) {
		t.Errorf("%s is still on disk after the claim", EnvFile)
	}
	if _, err := os.Stat(filepath.Join(r.incoming(), ".recipe-triage-2")); err != nil {
		t.Error("a task still being written was touched")
	}
}

func TestLaunchFailureIsAnExitCode(t *testing.T) {
	r := localRemote{t.TempDir()}
	submit(t, r, "recipe-triage-1", "", time.Now())
	ClaimAll(context.Background(), r.incoming(), r.tasks(), func(string, map[string]string) error {
		return errors.New("no shell")
	})
	if e, err := Find(listTasks(t, r), "recipe-triage-1", ""); err != nil || e.State != Exited {
		t.Fatalf("task = %+v, %v; want exited", e, err)
	}
}

// The whole round trip with the launcher the daemon uses: the client
// submits, the daemon claims and starts, and the task leaves the files
// the task server reads.
func TestRoundTrip(t *testing.T) {
	r := localRemote{t.TempDir()}
	stub := filepath.Join(t.TempDir(), "factory")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\necho \"args: $*\"\necho \"token: $GITHUB_TOKEN\"\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	submit(t, r, "recipe-triage-1", "", time.Now())
	ClaimAll(ctx, r.incoming(), r.tasks(), ExecLauncher(ctx, stub, r.root))

	tf := taskfiles.NewTaskFiles(filepath.Join(r.tasks(), "recipe-triage-1"))
	deadline := time.Now().Add(5 * time.Second)
	for !nonEmpty(tf.ExitCodeFile) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	code, _ := os.ReadFile(tf.ExitCodeFile)
	if strings.TrimSpace(string(code)) != "3" {
		t.Errorf("exit_code = %q, want 3", code)
	}
	log, _ := os.ReadFile(tf.LogFile)
	if !strings.Contains(string(log), "recipe exec --recipe "+tf.TaskDir+"/recipe.yaml") || !strings.Contains(string(log), "token: secret") {
		t.Errorf("log = %q, want the recipe exec command run with the submitted env", log)
	}
	if !nonEmpty(tf.PIDFile) || !nonEmpty(tf.StartTimeFile) {
		t.Error("pid/start_time missing: attaching and the kill commands need them")
	}
}

func TestFailUnstarted(t *testing.T) {
	r := localRemote{t.TempDir()}
	submit(t, r, "recipe-triage-1", "", time.Now())
	if err := os.MkdirAll(r.tasks(), 0o755); err != nil {
		t.Fatal(err)
	}
	// Claimed by a daemon that died before starting it.
	if err := os.Rename(filepath.Join(r.incoming(), "recipe-triage-1"), filepath.Join(r.tasks(), "recipe-triage-1")); err != nil {
		t.Fatal(err)
	}
	// An envd task: not the spool's to touch.
	if err := os.MkdirAll(filepath.Join(r.tasks(), "triage-20261002"), 0o755); err != nil {
		t.Fatal(err)
	}
	FailUnstarted(context.Background(), r.tasks())

	if !nonEmpty(filepath.Join(r.tasks(), "recipe-triage-1", "exit_code")) {
		t.Error("an unstarted spooled task has no exit code")
	}
	if _, err := os.Stat(filepath.Join(r.tasks(), "recipe-triage-1", EnvFile)); !os.IsNotExist(err) {
		t.Errorf("%s left on disk", EnvFile)
	}
	if nonEmpty(filepath.Join(r.tasks(), "triage-20261002", "exit_code")) {
		t.Error("an envd task was failed")
	}
}

func TestListAndFind(t *testing.T) {
	r := localRemote{t.TempDir()}
	now := time.Now()
	submit(t, r, "a", "client-1", now.Add(-2*time.Minute))
	submit(t, r, "b", "client-2", now.Add(-time.Minute))
	ClaimAll(context.Background(), r.incoming(), r.tasks(), func(taskDir string, _ map[string]string) error {
		return os.WriteFile(taskfiles.NewTaskFiles(taskDir).ExitCodeFile, []byte("0\n"), 0o644)
	})
	submit(t, r, "c", "client-1", now)
	// A recipe that is its sandbox's main task lists as that task type.
	spoolTask(t, r, Task{ID: "d", Recipe: "plan", TaskType: "plan", SubmittedAt: now.Add(-3 * time.Minute)}, []byte("name: plan\n"), nil, nil)
	// Tasks envd started: no task.json, kind and time from their names.
	// fix is running (its pid is this test's); plan's process is gone
	// without an exit code, as after a sandbox restart.
	fix := "fix-" + now.Add(-time.Hour).Format("20060102-150405")
	plan := "plan-" + now.Add(-2*time.Hour).Format("20060102-150405")
	gone := exec.Command("true")
	if err := gone.Run(); err != nil {
		t.Fatal(err)
	}
	for name, pid := range map[string]int{fix: os.Getpid(), plan: gone.Process.Pid} {
		if err := os.MkdirAll(filepath.Join(r.tasks(), name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(taskfiles.NewTaskFiles(filepath.Join(r.tasks(), name)).PIDFile, []byte(fmt.Sprintf("%d\n", pid)), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	entries := listTasks(t, r)
	var got []string
	for _, e := range entries {
		got = append(got, e.ID+":"+e.Kind+":"+string(e.State)+e.ExitCode)
	}
	if want := "c:recipe-triage:pending b:recipe-triage:exited0 a:recipe-triage:exited0 d:plan:pending " + fix + ":fix:running " + plan + ":plan:exited137"; strings.Join(got, " ") != want {
		t.Errorf("list = %v", got)
	}
	if code, _ := os.ReadFile(taskfiles.NewTaskFiles(filepath.Join(r.tasks(), plan)).ExitCodeFile); strings.TrimSpace(string(code)) != "137" {
		t.Errorf("dead task's exit_code = %q, want 137 recorded", code)
	}
	for _, tc := range []struct{ id, client, want string }{
		{"a", "", "a"},
		{"", "client-1", "c"},
		{"", "client-2", "b"},
		{"", "", "c"},
	} {
		e, err := Find(entries, tc.id, tc.client)
		if err != nil || e.ID != tc.want {
			t.Errorf("Find(%q, %q) = %q, %v; want %q", tc.id, tc.client, e.ID, err, tc.want)
		}
	}
	if _, err := Find(entries, "", "nobody"); err == nil {
		t.Error("Find of an unknown run name succeeded")
	}
}

func TestTaskJSONRoundTrips(t *testing.T) {
	in := Task{ID: "x", RunName: "c", Recipe: "triage", URL: "u", SubmittedAt: time.Unix(100, 0).UTC()}
	b, _ := json.Marshal(in)
	var out Task
	if err := json.Unmarshal(b, &out); err != nil || out != in {
		t.Errorf("round trip = %+v, %v", out, err)
	}
}
