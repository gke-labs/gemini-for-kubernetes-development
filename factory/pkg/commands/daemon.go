package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/acpd"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/sandbox"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/spool"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/taskapi"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/taskfiles"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

func NewDaemonCommand(ctx context.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:    "daemon",
		Short:  "Run the factory sandbox daemon: workspace storage, the spool and the task server",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("daemon command does not take any arguments")
			}
			return runDaemon(cmd.Context())
		},
	}
	return cmd
}

func runDaemon(ctx context.Context) error {
	log := klog.FromContext(ctx)

	// Ensure cache and temporary directories exist on /workspaces
	// This is important for Go builds to avoid ephemeral storage exhaustion.
	dirs := []string{
		sandbox.GoCachePath,
		sandbox.GoModCachePath,
		sandbox.TmpDirPath,
		os.Getenv("GOCACHE"),
		os.Getenv("GOMODCACHE"),
		os.Getenv("TMPDIR"),
		os.Getenv("GOTMPDIR"),
	}
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		if err := os.MkdirAll(dir, 0755); err != nil {
			log.Error(err, "failed to create directory", "path", dir)
		} else {
			log.Info("Initialized directory", "path", dir)
		}
	}

	// Mark any tasks interrupted by a previous container crash/eviction/restart
	// before any new processes/threads can reuse old PIDs.
	// Tasks with a status first, so theirs records why they ended.
	for _, id := range spool.MarkInterrupted(taskfiles.DefaultTasksDir) {
		log.Info("Marked a task of the previous container ended (exit_code=137)", "task", id)
	}
	reconcileInterruptedTasks(ctx, taskfiles.DefaultTasksDir)

	// Recipes a client left in the spool: claimed and started here, so the
	// client need not stay connected to start or keep them.
	factoryBin, err := os.Executable()
	if err != nil {
		factoryBin = "factory"
	}
	// Every task gets a token, which drives its agent session.
	tokens := taskapi.NewTokens()
	launch := tokens.Wrap(spool.ExecLauncher(ctx, factoryBin, sandbox.WorkspacesPath))
	go spool.Watch(ctx, spool.IncomingDir, taskfiles.DefaultTasksDir, 2*time.Second, launch)

	// One registry of agent sessions, whichever way they are reached: two
	// would each start an engine for the same id, writing one transcript.
	sessions := acpd.NewServer(acpd.StateDirFromEnv(), sandbox.WorkspacesPath)
	go func() {
		<-ctx.Done()
		sessions.Close()
	}()

	// The same tasks over HTTP, and the agent sessions with them, on the
	// loopback only: clients come in through a port-forward, which the
	// API server authorises.
	server := taskapi.NewServer(spool.IncomingDir, taskfiles.DefaultTasksDir, launch)
	server.HostSessions(sessions, tokens)
	go func() {
		if err := taskapi.Serve(ctx, fmt.Sprintf("127.0.0.1:%d", taskapi.Port), server); err != nil {
			log.Error(err, "task server exited")
		}
	}()

	// Start periodic cleanup in background
	go startPeriodicCleanup(ctx)

	// The pod lives as long as this process: everything above runs until
	// the container is stopped.
	log.Info("Sandbox daemon running")
	<-ctx.Done()
	return nil
}

// reconcileInterruptedTasks sweeps tasksDir on container startup (factory daemon
// runs as PID 1) and writes exit_code=137 for any task directory
// that has a recorded pid or start_time from a previous container lifecycle but no
// exit_code file yet.
func reconcileInterruptedTasks(ctx context.Context, tasksDir string) {
	log := klog.FromContext(ctx)
	entries, err := os.ReadDir(tasksDir)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Error(err, "failed to read tasks directory for startup reconciliation", "path", tasksDir)
		}
		return
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		taskPath := filepath.Join(tasksDir, entry.Name())
		tf := taskfiles.NewTaskFiles(taskPath)

		if hasNonEmptyFile(tf.ExitCodeFile) {
			continue
		}
		if !hasNonEmptyFile(tf.PIDFile) && !hasNonEmptyFile(tf.StartTimeFile) {
			continue
		}

		if err := os.WriteFile(tf.ExitCodeFile, []byte("137\n"), 0644); err != nil {
			log.Error(err, "failed to write exit_code for interrupted task", "task", entry.Name())
			continue
		}
		log.Info("Marked interrupted task from previous container lifecycle as crashed (exit_code=137)", "task", entry.Name())
	}
}

func hasNonEmptyFile(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(data)) != ""
}
