package tasks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/sandbox"
	"k8s.io/klog/v2"
)

type Task interface {
	PreScript() ([]byte, error)
	Prompt() ([]byte, error)
	PostScript() ([]byte, error)
}

func taskPath(taskDir string, name string, args ...interface{}) string {
	// Ensure the task path is correctly joined
	file := fmt.Sprintf(name, args...)
	return filepath.Join(taskDir, file)
}

func RunTask(ctx context.Context, t Task, sb *sandbox.IssueSandbox, taskDir string, env map[string]string) error {
	log := klog.FromContext(ctx)
	// Implementation of task execution logic would go here.
	taskScript, err := t.PreScript()
	if err != nil {
		return err
	}

	prompt, err := t.Prompt()
	if err != nil {
		return err
	}

	postScript, err := t.PostScript()
	if err != nil {
		return err
	}

	log.Info("copying prompt into sandbox", "sandbox", sb.GetSandboxID())
	promptPath := taskPath(taskDir, "agent-prompt.txt")
	if err := sb.WriteFile(promptPath, prompt); err != nil {
		return fmt.Errorf("copying prompt into sandbox: %w", err)
	}
	log.Info("Copied prompt into sandbox", "sandbox", sb.GetSandboxID(), "path", promptPath)

	log.Info("copying pre-script into sandbox", "sandbox", sb.GetSandboxID())
	preScriptPath := taskPath(taskDir, "pre-script.sh")
	if err := sb.WriteXFile(preScriptPath, taskScript); err != nil {
		return fmt.Errorf("copying pre-script into sandbox: %w", err)
	}
	log.Info("Copied pre-script into sandbox", "sandbox", sb.GetSandboxID(), "path", preScriptPath)

	postScriptPath := ""
	if postScript != nil {
		log.Info("copying post-script into sandbox", "sandbox", sb.GetSandboxID())
		postScriptPath = taskPath(taskDir, "post-script.sh")
		if err := sb.WriteXFile(postScriptPath, postScript); err != nil {
			return fmt.Errorf("copying post-script into sandbox: %w", err)
		}
		log.Info("Copied post-script into sandbox", "sandbox", sb.GetSandboxID(), "path", postScriptPath)
	}

	// Run the pre-script
	log.Info("running pre-script in sandbox", "sandbox", sb.GetSandboxID())
	opts := sandbox.ExecOptions{
		Command: []string{preScriptPath},
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Env:     env,
	}
	if err := sb.Exec(opts); err != nil {
		return fmt.Errorf("running gemini: %w", err)
	}
	log.Info("Completed pre-script in sandbox", "sandbox", sb.GetSandboxID())

	// Run the post-script if it exists
	if postScriptPath != "" {
		log.Info("running post-script in sandbox", "sandbox", sb.GetSandboxID())
		opts := sandbox.ExecOptions{
			Command: []string{postScriptPath},
			Stdout:  os.Stdout,
			Stderr:  os.Stderr,
			Env:     env,
		}
		if err := sb.Exec(opts); err != nil {
			return fmt.Errorf("running post-script: %w", err)
		}
		log.Info("Completed post-script in sandbox", "sandbox", sb.GetSandboxID())
	}

	return nil
}
