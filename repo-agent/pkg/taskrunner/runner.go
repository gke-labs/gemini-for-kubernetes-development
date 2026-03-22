package taskrunner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	sandboxtaskv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/sandboxtask/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/agentoutput"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/agentserver"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/llm"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/sandbox"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

type TaskRunner struct {
	manager     *k8s.Manager
	namespace   string
	sandboxName string
	ao          *agentoutput.AgentOutput
}

func NewTaskRunner(ao *agentoutput.AgentOutput) (*TaskRunner, error) {
	kubeClient, err := clients.NewKubernetesClient()
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	ns := os.Getenv("NAMESPACE")
	name := os.Getenv("NAME")

	if ns == "" || name == "" {
		return nil, fmt.Errorf("NAMESPACE and NAME environment variables must be set")
	}

	// Ensure cache and tmp directories exist on /workspaces
	// This is important for Go builds to avoid ephemeral storage exhaustion
	// and to allow caching across task runs.
	dirs := []string{
		os.Getenv("GOCACHE"),
		os.Getenv("GOMODCACHE"),
		os.Getenv("TMPDIR"),
	}
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		if err := os.MkdirAll(dir, 0755); err != nil {
			klog.Errorf("Failed to create directory %s: %v", dir, err)
		}
	}

	return &TaskRunner{
		manager:     k8s.NewManager(kubeClient),
		namespace:   ns,
		sandboxName: name,
		ao:          ao,
	}, nil
}

func (tr *TaskRunner) Run(ctx context.Context) {
	klog.Infof("Starting TaskRunner for sandbox: %s/%s", tr.namespace, tr.sandboxName)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tr.processPendingTasks(ctx)
		}
	}
}

func (tr *TaskRunner) processPendingTasks(ctx context.Context) {

	// List tasks with label selector using k8s manager
	tasks, err := tr.manager.ListSandboxTasks(ctx, tr.namespace, tr.sandboxName)
	if err != nil {
		klog.Errorf("Failed to list tasks: %v", err)
		return
	}

	for _, task := range tasks.Items {
		taskState := task.Status.TaskState
		if taskState == "" || taskState == "Pending" || taskState == "Running" {
			tr.executeTask(ctx, &task)
			// Process one task at a time for now
			return
		}
		klog.Infof("Skipping task %s with state %s", task.GetName(), taskState)
	}
}

func (tr *TaskRunner) createTaskDir(taskName string) (string, error) {
	taskDir := filepath.Join("/workspaces", ".agent", "tasks", taskName)
	// Create task directory if it doesn't exist
	// check if it exists
	if _, err := os.Stat(taskDir); os.IsNotExist(err) {
		err := os.MkdirAll(taskDir, 0755)
		if err != nil {
			klog.Errorf("Failed to create task directory %s: %v", taskDir, err)
			return "", err
		}
	}
	return taskDir, nil
}

// executeTask handles the execution of a single task
func (tr *TaskRunner) executeTask(ctx context.Context, task *sandboxtaskv1alpha1.SandboxTask) {
	taskName := task.GetName()
	klog.Infof("Processing task: %s", taskName)

	// Update status to Running
	tr.updateTaskStatus(ctx, task, "Running", "", nil)

	taskType := task.Spec.Type
	params := task.Spec.Params

	// Set sandbox state to Running Task
	_ = tr.ao.SetAgentState(ctx, "Working on "+taskType, "")

	// Gather traceability metadata
	repoWatchName := ""
	sbObj, err := tr.manager.Client.Resource(k8s.SandboxGVR).Namespace(tr.namespace).Get(ctx, tr.sandboxName, v1.GetOptions{})
	if err == nil {
		labels := sbObj.GetLabels()
		if labels != nil {
			repoWatchName = labels["review.gemini.google.com/repowatch"]
		}
	} else if !apierrors.IsNotFound(err) {
		klog.Warningf("Failed to get Sandbox %s/%s for traceability metadata: %v", tr.namespace, tr.sandboxName, err)
	}

	traceabilityEnv := []string{
		fmt.Sprintf("SANDBOX_TASK_NAME=%s", taskName),
		fmt.Sprintf("SANDBOX_TASK_UID=%s", string(task.GetUID())),
		fmt.Sprintf("SANDBOX_NAME=%s", tr.sandboxName),
		fmt.Sprintf("REPOWATCH_NAME=%s", repoWatchName),
		fmt.Sprintf("TASK_TYPE=%s", taskType),
	}

	logFile := filepath.Join(agentserver.LogsDirectory, taskName+".log")
	f, err := os.Create(logFile)
	if err != nil {
		klog.Errorf("Failed to create log file: %v", err)
		tr.updateTaskStatus(ctx, task, "Failed", fmt.Sprintf("failed to create log file: %v", err), nil)
		return
	}
	defer f.Close()

	var cmd *exec.Cmd

	taskDir, err := tr.createTaskDir(taskName)
	if err != nil {
		tr.updateTaskStatus(ctx, task, "Failed", err.Error(), nil)
		return
	}

	switch taskType {
	case "review":
		cmd = exec.Command(sandbox.RepoSandboxBinary, "review")
		// Map params to env vars
		cmd.Env = os.Environ()
		cmd.Env = append(cmd.Env, "AGENT_OUTPUT_GVR_RESOURCE=sandboxtasks")
		cmd.Env = append(cmd.Env, "AGENT_OUTPUT_GVR_GROUP=custom.agents.x-k8s.io")
		cmd.Env = append(cmd.Env, "AGENT_OUTPUT_GVR_VERSION=v1alpha1")
		// Inject params into env
		for k, v := range params {
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", strings.ToUpper(k), v))
		}

	case "fix-issue":
		cmd = exec.Command(sandbox.RepoSandboxBinary, "github-fix-issue", "--in-pod=true")
		// Map params to env vars
		cmd.Env = os.Environ()
		// Inject params into env
		for k, v := range params {
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", strings.ToUpper(k), v))
		}

	case "address-feedback":
		cmd = exec.Command(sandbox.RepoSandboxBinary, "github-feedback", "--in-pod=true")
		// Map params to env vars
		cmd.Env = os.Environ()
		// Inject params into env
		for k, v := range params {
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", strings.ToUpper(k), v))
		}

	case "investigate-failures":
		cmd = exec.Command(sandbox.RepoSandboxBinary, "github-investigate", "--in-pod=true")
		// Map params to env vars
		cmd.Env = os.Environ()
		// Inject params into env
		for k, v := range params {
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", strings.ToUpper(k), v))
		}

	case "triage-issue":
		cmd = exec.Command(sandbox.RepoSandboxBinary, "github-triage-issue", "--in-pod=true")
		// Map params to env vars
		cmd.Env = os.Environ()
		// Inject params into env
		for k, v := range params {
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", strings.ToUpper(k), v))
		}

	case "dev-setup":
		cmd = exec.Command(sandbox.RepoSandboxBinary, "dev-init", "--in-pod=true")
		// Map params to env vars
		cmd.Env = os.Environ()
		// Inject params into env
		for k, v := range params {
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", strings.ToUpper(k), v))
		}

	case "iterate":
		cmd = exec.Command(sandbox.RepoSandboxBinary, "iterate", "--in-pod=true")
		// Map params to env vars
		cmd.Env = os.Environ()
		// Inject params into env
		for k, v := range params {
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", strings.ToUpper(k), v))
		}

	case "chore":
		cmd = exec.Command(sandbox.RepoSandboxBinary, "chore", "--in-pod=true")
		// Map params to env vars
		cmd.Env = os.Environ()
		// Inject params into env
		for k, v := range params {
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", strings.ToUpper(k), v))
		}

	case "rollback":
		cmd = exec.Command(sandbox.RepoSandboxBinary, "rollback", "--in-pod=true")
		// Map params to env vars
		cmd.Env = os.Environ()
		// Inject params into env
		for k, v := range params {
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", strings.ToUpper(k), v))
		}

	case "issue":
		cmd = exec.Command(sandbox.RepoSandboxBinary, "dev")
		// Map params to env vars
		cmd.Env = os.Environ()
		cmd.Env = append(cmd.Env, "AGENT_OUTPUT_GVR_RESOURCE=sandboxtasks")
		cmd.Env = append(cmd.Env, "AGENT_OUTPUT_GVR_GROUP=custom.agents.x-k8s.io")
		cmd.Env = append(cmd.Env, "AGENT_OUTPUT_GVR_VERSION=v1alpha1")
		// Inject params into env
		for k, v := range params {
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", strings.ToUpper(k), v))
		}

	// TODO (barney-s): Pending decision: Should we support script tasks ?
	case "script":
		if command, ok := params["command"]; ok {
			cmd = exec.Command("/bin/sh", "-c", command)
			cmd.Env = os.Environ()
		} else {
			tr.updateTaskStatus(ctx, task, "Failed", "missing 'command' param", nil)
			return
		}

	default:
		klog.Warningf("Unknown task type: %s", taskType)
		tr.updateTaskStatus(ctx, task, "Failed", "unknown task type", nil)
		return
	}

	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	cmd.Env = append(cmd.Env, traceabilityEnv...)

	cmd.Env = append(cmd.Env, fmt.Sprintf("NAME=%s", taskName))
	cmd.Env = append(cmd.Env, fmt.Sprintf("TASKDIR=%s", taskDir))
	cmd.Stdout = io.MultiWriter(f, os.Stdout)
	cmd.Stderr = io.MultiWriter(f, os.Stderr)

	klog.Infof("Starting command for task %s", taskName)
	if err := cmd.Start(); err != nil {
		klog.Errorf("Failed to start command: %v", err)
		tr.updateTaskStatus(ctx, task, "Failed", err.Error(), nil)
		return
	}

	if err := cmd.Wait(); err != nil {
		klog.Errorf("Command failed: %v", err)
		usage := tr.readStats(taskDir)
		tr.updateTaskStatus(ctx, task, "Failed", err.Error(), usage)
		_ = tr.ao.SetAgentState(ctx, "Failed "+taskType, err.Error())
	} else {
		klog.Infof("Task %s completed successfully", taskName)
		usage := tr.readStats(taskDir)
		tr.updateTaskStatus(ctx, task, "Completed", "", usage)
		// Set sandbox state to Running Task
		_ = tr.ao.SetAgentState(ctx, taskType+" Ready", "")
	}
}

func (tr *TaskRunner) updateTaskStatus(ctx context.Context, task *sandboxtaskv1alpha1.SandboxTask, state, result string, stats *sandboxtaskv1alpha1.Stats) {
	if err := tr.manager.UpdateSandboxTaskStatus(ctx, tr.namespace, task.GetName(), state, result, stats); err != nil {
		klog.Errorf("Failed to update task status: %v", err)
	}
}

func (tr *TaskRunner) readStats(taskDir string) *sandboxtaskv1alpha1.Stats {
	usagePath := filepath.Join(taskDir, "llm-usage.json")
	data, err := os.ReadFile(usagePath)
	if err != nil {
		klog.V(2).Infof("No llm-usage.json found in %s: %v", taskDir, err)
		return nil
	}

	// Clean up the file after reading to avoid disk accumulation.
	defer func() {
		if err := os.Remove(usagePath); err != nil {
			klog.Warningf("Failed to remove llm-usage.json after reading: %v", err)
		}
	}()

	// The file is written by the llm package as llm.Stats, convert to CRD type.
	var usage llm.Stats
	if err := json.Unmarshal(data, &usage); err != nil {
		klog.Warningf("Failed to unmarshal llm-usage.json: %v", err)
		return nil
	}

	if len(usage.Models) == 0 {
		return nil
	}

	crdUsage := &sandboxtaskv1alpha1.Stats{
		Models: make(map[string]sandboxtaskv1alpha1.ModelUsage, len(usage.Models)),
	}
	for model, data := range usage.Models {
		crdUsage.Models[model] = sandboxtaskv1alpha1.ModelUsage{
			TotalRequests:  data.API.TotalRequests,
			TotalErrors:    data.API.TotalErrors,
			TotalLatencyMs: data.API.TotalLatencyMs,
			InputTokens:    data.Tokens.Input,
			OutputTokens:   data.Tokens.Output,
			TotalTokens:    data.Tokens.Total,
			CachedTokens:   data.Tokens.Cached,
			ThoughtTokens:  data.Tokens.Thoughts,
		}
	}
	return crdUsage
}
