package tasks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/sandboxtask/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/agentoutput"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/sandbox"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"
)

type Task interface {
	PreScript() ([]byte, error)
	Prompt() ([]byte, error)
	PostScript() ([]byte, error)
}

func taskPath(taskDir string, name string) string {
	// Ensure the task path is correctly joined
	return filepath.Join(taskDir, name)
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

	// Read agent output and update annotation
	agentOutputPath := taskPath(taskDir, "agent-output.txt")
	output, err := sb.ReadFile(agentOutputPath)
	if err == nil && len(output) > 0 {
		log.Info("Read agent output", "output", string(output))

		// Check if we have env vars for AgentOutput
		gvr := schema.GroupVersionResource{
			Group:    v1alpha1.GroupVersion.Group,
			Version:  v1alpha1.GroupVersion.Version,
			Resource: "sandboxtasks",
		}
		ao, err := agentoutput.New(gvr, "", "")
		if err != nil {
			log.Error(err, "Failed to create agent output client")
		} else {
			if err := ao.SetAgentDraft(ctx, string(output)); err != nil {
				log.Error(err, "Failed to set agent draft")
			}
		}
	} else if err != nil {
		log.Info("Failed to read agent output (might be missing)", "path", agentOutputPath, "err", err)
	}

	return nil
}
