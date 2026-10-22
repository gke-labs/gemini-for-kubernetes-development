package dispatcher

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/recipe"
)

// CLIRunnerConfig holds the sandbox and resource settings forwarded to the child factory CLI.
type CLIRunnerConfig struct {
	Namespace        string
	Image            string
	DiskSize         string
	StorageClass     string
	EphemeralStorage string
	CPURequest       string
	CPULimit         string
	MemoryRequest    string
	MemoryLimit      string
	// TaskTimeout is forwarded to the child process as --timeout. Zero omits the flag.
	TaskTimeout time.Duration
	// ProcessingLogDir is where the per-task log file is written while the task runs.
	ProcessingLogDir string
}

// CLIRunner executes tasks by spawning a child `factory` CLI process.
//
// It implements TaskRunner and is the only component that knows how a queue
// task maps onto a factory command line, which keeps process execution details
// out of the dispatcher and allows tests to substitute a fake TaskRunner.
type CLIRunner struct {
	cfg CLIRunnerConfig

	// execCommand builds the child process. Defaults to exec.CommandContext and is overridable in tests.
	execCommand func(ctx context.Context, name string, args ...string) *exec.Cmd
	// resolveExecutable resolves the path of the factory binary. Defaults to os.Executable and is overridable in tests.
	resolveExecutable func() (string, error)
}

// NewCLIRunner constructs a CLIRunner that shells out to the running factory binary.
func NewCLIRunner(cfg CLIRunnerConfig) *CLIRunner {
	return &CLIRunner{
		cfg:               cfg,
		execCommand:       exec.CommandContext,
		resolveExecutable: os.Executable,
	}
}

// BuildArgs returns the factory CLI arguments for a task, or nil if the
// task type is unknown or, for a recipe task, no rule decided it (no recipe).
func (r *CLIRunner) BuildArgs(t *api.QueueTask, selectedUser string) []string {
	var args []string
	if rt, ok := api.RecipeTaskOf(t.Type); ok {
		// The recipe the rule that decided t names, its result applied
		// by the CLI as the user picked (--user); none without one. The
		// run name is this enqueue's: a restarted watcher's recovered
		// task follows its run, or applies its result, and a task
		// enqueued again after a failure starts a new one.
		if t.Recipe == "" {
			return nil
		}
		args = []string{"recipe", t.Recipe, "--url", t.URL, "--run-name", rt.RunName(t)}
		if _, _, err := recipe.Builtin(t.Recipe); err != nil {
			// A repository's recipe has no command of its name.
			args = []string{"recipe", "run", "--recipe", t.Recipe, "--url", t.URL, "--run-name", rt.RunName(t)}
		}
		args = append(args, api.RecipeArgs(t)...)
		for _, inst := range t.Instructions {
			args = append(args, "--instruction", inst)
		}
		return r.withCommonArgs(args, selectedUser)
	}
	switch t.Type {
	case api.TypeAgentChore:
		// Named as a recipe task's run is: a restarted watcher's
		// recovered chore follows its run, or applies its result.
		args = []string{"agent", "create", "--url", t.URL, "--agent", t.AgentFile, "--run-name", api.ChoreRunName(t)}
		if t.SessionID != "" {
			args = append(args, "--session-id", t.SessionID)
		}
	default:
		return nil
	}
	return r.withCommonArgs(args, selectedUser)
}

// withCommonArgs appends the flags every task's command takes: the
// namespace, the user, and the sandbox's image and resources.
func (r *CLIRunner) withCommonArgs(args []string, selectedUser string) []string {
	if r.cfg.Namespace != "" {
		args = append(args, "--namespace", r.cfg.Namespace)
	}
	if selectedUser != "" {
		args = append(args, "--user", selectedUser)
	}
	if r.cfg.Image != "" {
		args = append(args, "--image", r.cfg.Image)
	}
	if r.cfg.DiskSize != "" {
		args = append(args, "--workspace-disk-size", r.cfg.DiskSize)
	}
	if r.cfg.StorageClass != "" {
		args = append(args, "--workspace-storage-class", r.cfg.StorageClass)
	}
	if r.cfg.EphemeralStorage != "" {
		args = append(args, "--ephemeral-storage", r.cfg.EphemeralStorage)
	}
	if r.cfg.CPURequest != "" {
		args = append(args, "--cpu-request", r.cfg.CPURequest)
	}
	if r.cfg.CPULimit != "" {
		args = append(args, "--cpu-limit", r.cfg.CPULimit)
	}
	if r.cfg.MemoryRequest != "" {
		args = append(args, "--memory-request", r.cfg.MemoryRequest)
	}
	if r.cfg.MemoryLimit != "" {
		args = append(args, "--memory-limit", r.cfg.MemoryLimit)
	}
	if r.cfg.TaskTimeout > 0 {
		args = append(args, "--timeout", r.cfg.TaskTimeout.String())
	}
	args = append(args, "--abort-on-cancel=false")

	return args
}

// Run executes the task in a child factory process, streaming its output to the
// task log file. It returns an error if the task could not be started or exited non-zero.
func (r *CLIRunner) Run(ctx context.Context, taskFilename string, task *api.QueueTask, selectedUser string) error {
	args := r.BuildArgs(task, selectedUser)
	if args == nil {
		if _, ok := api.RecipeTaskOf(task.Type); ok {
			return fmt.Errorf("task %s carries no recipe: no rule decided it", taskFilename)
		}
		return fmt.Errorf("unknown task type: %s", task.Type)
	}

	executable, err := r.resolveExecutable()
	if err != nil {
		return fmt.Errorf("resolving factory executable: %w", err)
	}

	cmd := r.execCommand(ctx, executable, args...)

	logPath := ""
	if logFile, err := r.openLogFile(taskFilename); err != nil {
		klog.Errorf("Failed to create log file: %v", err)
	} else {
		defer logFile.Close()
		cmd.Stdout = logFile
		cmd.Stderr = logFile
		logPath = logFile.Name()
	}

	err = cmd.Run()
	if err == nil || logPath == "" {
		return err
	}
	// What the child printed as its error, rather than its exit status
	// alone: a run that succeeded in its sandbox and failed applying its
	// result says so only in its log.
	return &RunError{Err: err, Reason: failureFromLog(logPath)}
}

// openLogFile creates the log file that a running task streams its output into.
func (r *CLIRunner) openLogFile(taskFilename string) (*os.File, error) {
	logFilename := strings.TrimSuffix(taskFilename, ".yaml") + ".log"
	logPath := filepath.Join(r.cfg.ProcessingLogDir, logFilename)
	return os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
}
