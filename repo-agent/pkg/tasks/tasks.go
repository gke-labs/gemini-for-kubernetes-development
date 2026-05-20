package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/sandboxtask/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/agentoutput"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/llm"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/sandbox"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"
)

type Task interface {
	PreScript() ([]byte, error)
	Prompt() ([]byte, error)
	PostScript() ([]byte, error)
	DraftState() string
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

	// Create task directory if it doesn't exist
	if err := os.MkdirAll(taskDir, 0755); err != nil {
		return fmt.Errorf("failed to create task directory: %w", err)
	}

	// Create log file for task execution
	logFile := filepath.Join(taskDir, "execution.log")
	f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("failed to create log file: %w", err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			log.Error(err, "Failed to close log file")
		}
	}()

	_, _ = fmt.Fprintf(f, "\n--- Task started at %s ---\n", time.Now().Format(time.RFC3339))
	defer func() {
		_, _ = fmt.Fprintf(f, "--- Task completed at %s ---\n", time.Now().Format(time.RFC3339))
	}()

	// Run the pre-script
	log.Info("running pre-script in sandbox", "sandbox", sb.GetSandboxID())
	opts := sandbox.ExecOptions{
		Command: []string{preScriptPath},
		Stdout:  io.MultiWriter(f, os.Stdout),
		Stderr:  io.MultiWriter(f, os.Stderr),
		Env:     env,
	}
	if err := sb.Exec(opts); err != nil {
		return fmt.Errorf("running gemini: %w", err)
	}
	log.Info("Completed pre-script in sandbox", "sandbox", sb.GetSandboxID())

	// Process gemini JSON output: extract response text and LLM usage stats.
	processGeminiOutput(ctx, sb, taskDir)

	// Run the post-script if it exists
	if postScriptPath != "" {
		log.Info("running post-script in sandbox", "sandbox", sb.GetSandboxID())
		opts := sandbox.ExecOptions{
			Command: []string{postScriptPath},
			Stdout:  io.MultiWriter(f, os.Stdout),
			Stderr:  io.MultiWriter(f, os.Stderr),
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
			state := t.DraftState()
			if state == "" {
				state = "informational"
			}
			if err := ao.SetAgentDraftType(ctx, state); err != nil {
				log.Error(err, "Failed to set agent draft type")
			}
		}
	} else if err != nil {
		log.Info("Failed to read agent output (might be missing)", "path", agentOutputPath, "err", err)
	}

	return nil
}

// processGeminiOutput reads the gemini CLI JSON output (written when using
// --output-format json), extracts the response text to raw-agent-output.txt
// (for post-script processing) and the LLM usage stats to llm-usage.json
// (for the task runner to pick up).
func processGeminiOutput(ctx context.Context, sb *sandbox.IssueSandbox, taskDir string) {
	log := klog.FromContext(ctx)
	geminiOutputPath := taskPath(taskDir, "gemini-output.json")
	data, err := sb.ReadFile(geminiOutputPath)
	if err != nil || len(data) == 0 {
		log.V(2).Info("No gemini-output.json found (skipping)", "path", geminiOutputPath)
		return
	}

	response, stats, err := llm.ParseGeminiOutput(data)
	if err != nil {
		log.Error(err, "Failed to parse gemini output")
		return
	}

	// Write response text so post-scripts can process it (e.g. triage grep).
	if response != "" {
		responsePath := taskPath(taskDir, "raw-agent-output.txt")
		if err := os.WriteFile(responsePath, []byte(response), 0644); err != nil {
			log.Error(err, "Failed to write raw-agent-output.txt", "path", responsePath)
		}
	}

	if stats == nil {
		return
	}
	statsJSON, err := json.Marshal(stats)
	if err != nil {
		log.Error(err, "Failed to marshal LLM stats")
		return
	}
	usagePath := taskPath(taskDir, "llm-usage.json")
	if err := os.WriteFile(usagePath, statsJSON, 0644); err != nil {
		log.Error(err, "Failed to write llm-usage.json", "path", usagePath)
	} else {
		log.Info("Wrote LLM usage stats", "path", usagePath)
	}
}
