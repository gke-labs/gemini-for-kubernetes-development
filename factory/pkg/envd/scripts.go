package envd

import "fmt"

const DefaultTasksDir = "/workspaces/tasks"

// TaskFiles represents standard file paths for a resilient background task.
type TaskFiles struct {
	TaskDir       string
	PIDFile       string
	StartTimeFile string
	LogFile       string
	ExitCodeFile  string
}

// NewTaskFiles creates a TaskFiles struct for a given task directory.
func NewTaskFiles(taskDir string) TaskFiles {
	return TaskFiles{
		TaskDir:       taskDir,
		PIDFile:       fmt.Sprintf("%s/pid", taskDir),
		StartTimeFile: fmt.Sprintf("%s/start_time", taskDir),
		LogFile:       fmt.Sprintf("%s/execution.log", taskDir),
		ExitCodeFile:  fmt.Sprintf("%s/exit_code", taskDir),
	}
}

// BuildDetachedLaunchCmd generates the shell command to launch a detached task in the sandbox pod.
func BuildDetachedLaunchCmd(files TaskFiles, cmdStr string) string {
	return fmt.Sprintf("nohup sh -c \"echo \\$\\$ > %s; ps -p \\$\\$ -o lstart= > %s; %s > %s 2>&1; echo \\$? > %s\" >/dev/null 2>&1 &",
		files.PIDFile, files.StartTimeFile, cmdStr, files.LogFile, files.ExitCodeFile)
}

// BuildCheckPidCmd generates the shell command to check if a task process is alive, not a zombie, and matches start_time.
// Outputs "alive" if the process is currently running and start time matches.
func BuildCheckPidCmd(pidFile, startTimeFile string) string {
	return fmt.Sprintf("if [ -s %s ]; then pid=$(cat %s 2>/dev/null); if [ -n \"$pid\" ]; then stat=$(ps -o stat= -p \"$pid\" 2>/dev/null | cut -c 1); expected_start=$(cat %s 2>/dev/null | xargs); current_start=$(ps -p \"$pid\" -o lstart= 2>/dev/null | xargs); if kill -0 \"$pid\" 2>/dev/null && [ \"$stat\" != \"Z\" ] && { [ -z \"$expected_start\" ] || [ \"$expected_start\" = \"$current_start\" ]; }; then echo \"alive\"; fi; fi; fi",
		pidFile, pidFile, startTimeFile)
}

// BuildAbortKillCmd generates the shell command to terminate a task process tree on abort/cancel.
// Kills PID and its children if PID and start_time match, and writes exit code 143 if exit_code does not exist.
func BuildAbortKillCmd(pidFile, startTimeFile, exitCodeFile string) string {
	return fmt.Sprintf("if [ -f %s ]; then pid=$(cat %s 2>/dev/null); expected_start=$(cat %s 2>/dev/null | xargs); current_start=$(ps -p \"$pid\" -o lstart= 2>/dev/null | xargs); if [ -z \"$expected_start\" ] || [ \"$expected_start\" = \"$current_start\" ]; then pids=\"$pid $(pgrep -P $pid 2>/dev/null)\"; kill $pids 2>/dev/null || true; fi; if [ ! -f %s ]; then echo 143 > %s; fi; fi",
		pidFile, pidFile, startTimeFile, exitCodeFile, exitCodeFile)
}

// BuildQuotaKillCmd generates the shell command to terminate a task process group on quota/fatal error.
// Kills process group, children, or top PID if start_time matches, and writes exit code 137.
func BuildQuotaKillCmd(pidFile, startTimeFile, exitCodeFile string) string {
	return fmt.Sprintf("if [ -f %s ]; then top_pid=$(cat %s 2>/dev/null); expected_start=$(cat %s 2>/dev/null | xargs); current_start=$(ps -p \"$top_pid\" -o lstart= 2>/dev/null | xargs); if [ -z \"$expected_start\" ] || [ \"$expected_start\" = \"$current_start\" ]; then kill -9 -$(ps -o pgid= $top_pid 2>/dev/null | tr -d ' ') 2>/dev/null || pkill -9 -P $top_pid 2>/dev/null || kill -9 $top_pid 2>/dev/null || true; fi; echo 137 > %s; fi",
		pidFile, pidFile, startTimeFile, exitCodeFile)
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
		expected_start=$(cat "$task_dir/start_time" 2>/dev/null | xargs)
		current_start=$(ps -p "$pid" -o lstart= 2>/dev/null | xargs)
		if ! kill -0 "$pid" 2>/dev/null || [ "$stat" = "Z" ] || { [ -n "$expected_start" ] && [ "$expected_start" != "$current_start" ]; }; then
			echo "137" # Report SIGKILL/Crashed/Zombie/PID-recycled fallback exit code
		else
			echo "RUNNING"
		fi
	else
		echo "NOTASKS"
	fi
fi`, tasksParentDir)
}
