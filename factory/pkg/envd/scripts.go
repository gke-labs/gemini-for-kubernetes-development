package envd

import "fmt"

const DefaultTasksDir = "/workspaces/tasks"

// TaskFiles represents standard file paths for a resilient background task.
type TaskFiles struct {
	TaskDir      string
	PIDFile      string
	LogFile      string
	ExitCodeFile string
}

// NewTaskFiles creates a TaskFiles struct for a given task directory.
func NewTaskFiles(taskDir string) TaskFiles {
	return TaskFiles{
		TaskDir:      taskDir,
		PIDFile:      fmt.Sprintf("%s/pid", taskDir),
		LogFile:      fmt.Sprintf("%s/execution.log", taskDir),
		ExitCodeFile: fmt.Sprintf("%s/exit_code", taskDir),
	}
}

// BuildDetachedLaunchCmd generates the shell command to launch a detached task in the sandbox pod.
func BuildDetachedLaunchCmd(files TaskFiles, cmdStr string) string {
	return fmt.Sprintf("nohup sh -c \"echo \\$\\$ > %s; %s > %s 2>&1; echo \\$? > %s\" >/dev/null 2>&1 &",
		files.PIDFile, cmdStr, files.LogFile, files.ExitCodeFile)
}

// BuildCheckPidCmd generates the shell command to check if a task process is alive and not a zombie.
// Outputs "alive" if the process is currently running.
func BuildCheckPidCmd(pidFile string) string {
	return fmt.Sprintf("if [ -s %s ]; then pid=$(cat %s 2>/dev/null); if [ -n \"$pid\" ]; then stat=$(ps -o stat= -p \"$pid\" 2>/dev/null | cut -c 1); if kill -0 \"$pid\" 2>/dev/null && [ \"$stat\" != \"Z\" ]; then echo \"alive\"; fi; fi; fi",
		pidFile, pidFile)
}

// BuildAbortKillCmd generates the shell command to terminate a task process tree on abort/cancel.
func BuildAbortKillCmd(pidFile, exitCodeFile string) string {
	return fmt.Sprintf("if [ -f %s ]; then pids=\"$(cat %s 2>/dev/null) $(pgrep -P $(cat %s 2>/dev/null) 2>/dev/null)\"; kill $pids 2>/dev/null || true; if [ ! -f %s ]; then echo 143 > %s; fi; fi",
		pidFile, pidFile, pidFile, exitCodeFile, exitCodeFile)
}

// BuildQuotaKillCmd generates the shell command to terminate a task process group on quota/fatal error.
func BuildQuotaKillCmd(pidFile, exitCodeFile string) string {
	return fmt.Sprintf("if [ -f %s ]; then top_pid=$(cat %s 2>/dev/null); kill -9 -$(ps -o pgid= $top_pid 2>/dev/null | tr -d ' ') 2>/dev/null || pkill -9 -P $top_pid 2>/dev/null || kill -9 $top_pid 2>/dev/null || true; echo 137 > %s; fi",
		pidFile, pidFile, exitCodeFile)
}

// BuildTailLogCmd generates the shell command to read delta logs starting from offset.
func BuildTailLogCmd(logFile string, offset int64) string {
	return fmt.Sprintf("if [ -f %s ]; then tail -c +%d %s; fi", logFile, offset+1, logFile)
}

// BuildCheckExitCodeCmd generates the shell command to read exit code file.
func BuildCheckExitCodeCmd(exitCodeFile string) string {
	return fmt.Sprintf("if [ -s %s ]; then cat %s; fi", exitCodeFile, exitCodeFile)
}

// BuildWriteExitCodeCmd generates the shell command to write exit code unconditionally.
func BuildWriteExitCodeCmd(exitCodeFile string, exitCode int) string {
	return fmt.Sprintf("echo %d > %s", exitCode, exitCodeFile)
}

// BuildWriteExitCodeIfMissingCmd generates the shell command to write exit code only if the file does not exist.
func BuildWriteExitCodeIfMissingCmd(exitCodeFile string, exitCode int) string {
	return fmt.Sprintf("if [ ! -f %s ]; then echo %d > %s; fi", exitCodeFile, exitCode, exitCodeFile)
}

// BuildCheckLatestTaskStatusCmd generates the shell script to check the status of the most recent task directory.
func BuildCheckLatestTaskStatusCmd(tasksParentDir string) string {
	return fmt.Sprintf(`task_dir=$(ls -td %s/* 2>/dev/null | head -1)
if [ -z "$task_dir" ]; then
	echo "NOTASKS"
elif [ -s "$task_dir/exit_code" ]; then
	cat "$task_dir/exit_code"
else
	pid=$(cat "$task_dir/pid" 2>/dev/null)
	if [ -n "$pid" ]; then
		stat=$(ps -o stat= -p "$pid" 2>/dev/null | cut -c 1)
		if ! kill -0 "$pid" 2>/dev/null || [ "$stat" = "Z" ]; then
			echo "137" # Report SIGKILL/Crashed/Zombie fallback exit code
		else
			echo "RUNNING"
		fi
	else
		echo "NOTASKS"
	fi
fi`, tasksParentDir)
}
