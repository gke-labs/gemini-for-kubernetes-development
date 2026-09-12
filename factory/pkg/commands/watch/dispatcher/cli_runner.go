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
)

// CLIRunnerConfig holds the sandbox and resource settings forwarded to the child factory CLI.
type CLIRunnerConfig struct {
	Namespace        string
	Image            string
	DiskSize         string
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

// BuildArgs returns the factory CLI arguments for a task, or nil if the task type is unknown.
func (r *CLIRunner) BuildArgs(t *api.QueueTask, selectedUser string) []string {
	var args []string
	switch t.Type {
	case api.TypeIssueFix:
		args = []string{"fix", "--url", t.URL, "--instruction", "Fix this issue"}
	case api.TypePRInvestigate:
		args = []string{"pr", "investigate", "--pr-url", t.URL}
	case api.TypePRComments:
		args = []string{"pr", "address-comments", "--pr-url", t.URL}
	case api.TypePRIterate:
		args = []string{"pr", "iterate", "--pr-url", t.URL, "--prompt", "Please resolve merge conflicts in this PR by rebasing onto the latest master/main branch and resolving any conflicts that arise."}
	case api.TypePRReview:
		args = []string{"pr", "review", "--pr-url", t.URL, "--publish", "yes"}
		for _, inst := range t.Instructions {
			args = append(args, "--instruction", inst)
		}
	case api.TypeAgentChore:
		args = []string{"agent", "create", "--url", t.URL, "--agent", t.AgentFile}
		if t.SessionID != "" {
			args = append(args, "--session-id", t.SessionID)
		}
	default:
		return nil
	}

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
		return fmt.Errorf("unknown task type: %s", task.Type)
	}

	executable, err := r.resolveExecutable()
	if err != nil {
		return fmt.Errorf("resolving factory executable: %w", err)
	}

	cmd := r.execCommand(ctx, executable, args...)

	if logFile, err := r.openLogFile(taskFilename); err != nil {
		klog.Errorf("Failed to create log file: %v", err)
	} else {
		defer logFile.Close()
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	}

	return cmd.Run()
}

// openLogFile creates the log file that a running task streams its output into.
func (r *CLIRunner) openLogFile(taskFilename string) (*os.File, error) {
	logFilename := strings.TrimSuffix(taskFilename, ".yaml") + ".log"
	logPath := filepath.Join(r.cfg.ProcessingLogDir, logFilename)
	return os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
}
