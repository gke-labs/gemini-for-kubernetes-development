// Package spool runs recipes a client leaves in a sandbox, so that the
// client does not have to stay connected to start or keep them.
//
// A client writes a task directory into IncomingDir under a dot-name and
// renames it into place, so it appears whole. The sandbox's daemon (PID 1)
// claims it by renaming it to TasksDir/<id> and starts `factory recipe
// exec` there, with the same pid, start_time, execution.log and exit_code
// files envd-launched tasks have, so attaching to it (envd.AttachTask) and
// recovery after a restart work as they always have.
//
// Only recipes are spooled: the daemon builds the command from the task
// directory's recipe.yaml and inputs.json and never runs one it is given.
package spool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/envd"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/taskoutput"
	"k8s.io/klog/v2"
)

const (
	// Dir is outside envd.DefaultTasksDir on purpose: things list
	// tasks/* to find the newest task, and must not find the spool.
	Dir         = "/workspaces/spool"
	IncomingDir = Dir + "/incoming"
	// WithdrawnDir is where a client moves a task nobody claimed, to take
	// it back; a rename, so it fails if the daemon claimed it meanwhile.
	WithdrawnDir = Dir + "/withdrawn"

	TaskFile   = "task.json"
	RecipeFile = "recipe.yaml"
	InputsFile = "inputs.json"
	// EnvFile holds the task's environment, tokens included. The daemon
	// deletes it as soon as it has read it.
	EnvFile = "env.json"
)

// Task describes a spooled task. It stays in the task directory, so a
// client that reconnects can find its task again.
type Task struct {
	ID string `json:"id"`
	// RunName is whatever the submitting client wants to find the task
	// by later; factory does not interpret it.
	RunName string `json:"run_name,omitempty"`
	Recipe  string `json:"recipe"`
	// TaskType is the recipe's task-type, when it is its sandbox's main
	// task; unset, the task's kind is recipe-<Recipe>.
	TaskType    string    `json:"task_type,omitempty"`
	URL         string    `json:"url,omitempty"`
	SubmittedAt time.Time `json:"submitted_at"`
	// Output is the recipe's task-output declaration, which the recipe the
	// sandbox runs no longer has (recipe.ForSandbox).
	Output *taskoutput.Decl `json:"output,omitempty"`
}

// ExecCmd is the command that runs the recipe in taskDir, with factory
// the binary to run it.
func ExecCmd(factory, taskDir string) string {
	return fmt.Sprintf("%s recipe exec --recipe %s/%s --inputs %s/%s --task-dir %s",
		factory, taskDir, RecipeFile, taskDir, InputsFile, taskDir)
}

// Launcher starts the recipe in a claimed task directory with env added
// to the daemon's environment.
type Launcher func(taskDir string, env map[string]string) error

// Watch claims and launches tasks as they arrive in incomingDir until ctx
// ends. Tasks claimed by an earlier daemon that never started — it died
// between the claim and the launch — are failed first: their environment
// may already be gone.
func Watch(ctx context.Context, incomingDir, tasksDir string, interval time.Duration, launch Launcher) {
	log := klog.FromContext(ctx)
	if err := os.MkdirAll(incomingDir, 0o755); err != nil {
		log.Error(err, "spool: creating incoming directory; not watching", "path", incomingDir)
		return
	}
	FailUnstarted(ctx, tasksDir)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		ClaimAll(ctx, incomingDir, tasksDir, launch)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// ClaimAll claims every complete task in incomingDir, oldest name first,
// and launches it.
func ClaimAll(ctx context.Context, incomingDir, tasksDir string, launch Launcher) {
	log := klog.FromContext(ctx)
	entries, err := os.ReadDir(incomingDir)
	if err != nil {
		log.Error(err, "spool: reading incoming directory", "path", incomingDir)
		return
	}
	for _, e := range entries {
		// Dot-names are still being written.
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		taskDir := filepath.Join(tasksDir, e.Name())
		if err := os.MkdirAll(tasksDir, 0o755); err != nil {
			log.Error(err, "spool: creating tasks directory", "path", tasksDir)
			return
		}
		// The claim. A task directory of that name already existing means
		// a duplicate id; leave it for whoever submitted it to notice.
		if _, err := os.Stat(taskDir); err == nil {
			log.Info("spool: task directory already exists; not claiming", "task", e.Name())
			continue
		}
		if err := os.Rename(filepath.Join(incomingDir, e.Name()), taskDir); err != nil {
			log.Error(err, "spool: claiming task", "task", e.Name())
			continue
		}
		log.Info("spool: claimed task", "task", e.Name())
		if err := start(taskDir, launch); err != nil {
			log.Error(err, "spool: starting task", "task", e.Name())
			fail(taskDir, err)
		}
	}
}

func start(taskDir string, launch Launcher) error {
	envPath := filepath.Join(taskDir, EnvFile)
	env := map[string]string{}
	data, err := os.ReadFile(envPath)
	_ = os.Remove(envPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reading %s: %w", EnvFile, err)
	}
	if err == nil {
		if err := json.Unmarshal(data, &env); err != nil {
			return fmt.Errorf("parsing %s: %w", EnvFile, err)
		}
	}
	return launch(taskDir, env)
}

// fail gives a task that could not start the exit code and log a client
// attached to it reads.
func fail(taskDir string, cause error) {
	tf := envd.NewTaskFiles(taskDir)
	_ = os.WriteFile(tf.LogFile, []byte(fmt.Sprintf("spool: the task could not start: %v\n", cause)), 0o644)
	_ = os.WriteFile(tf.ExitCodeFile, []byte("127\n"), 0o644)
}

// FailUnstarted fails spooled tasks in tasksDir that were claimed but
// never started: a task.json, no pid and no exit code.
func FailUnstarted(ctx context.Context, tasksDir string) {
	entries, err := os.ReadDir(tasksDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		taskDir := filepath.Join(tasksDir, e.Name())
		tf := envd.NewTaskFiles(taskDir)
		if !e.IsDir() || !exists(filepath.Join(taskDir, TaskFile)) || nonEmpty(tf.PIDFile) || nonEmpty(tf.ExitCodeFile) {
			continue
		}
		_ = os.Remove(filepath.Join(taskDir, EnvFile))
		fail(taskDir, fmt.Errorf("claimed but not started before the sandbox restarted"))
		klog.FromContext(ctx).Info("spool: failed a task claimed but never started", "task", e.Name())
	}
}

// ExecLauncher starts tasks as children of this process, the sandbox's
// PID 1, running factory (this binary) in the task directory.
func ExecLauncher(ctx context.Context, factory, workDir string) Launcher {
	return func(taskDir string, env map[string]string) error {
		tf := envd.NewTaskFiles(taskDir)
		cmd := exec.Command("sh", "-c", envd.TaskScript(tf, ExecCmd(factory, taskDir)))
		cmd.Dir = workDir
		cmd.Env = os.Environ()
		for k, v := range env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		// Its own process group: the quota kill signals the task's whole
		// group, which must not be PID 1's.
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := cmd.Start(); err != nil {
			return err
		}
		go func() {
			// Reap it; the script records the exit code itself.
			_ = cmd.Wait()
			klog.FromContext(ctx).Info("spool: task exited", "task", filepath.Base(taskDir))
		}()
		return nil
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func nonEmpty(path string) bool {
	data, err := os.ReadFile(path)
	return err == nil && strings.TrimSpace(string(data)) != ""
}
