package taskrunner

import (
	"context"
	"fmt"
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
		if taskState == "" || taskState == "Pending" {
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
	tr.updateTaskStatus(ctx, task, "Running", "")

	taskType := task.Spec.Type
	params := task.Spec.Params

	// Set sandbox state to Running Task
	_ = tr.ao.SetAgentState(ctx, "Working on "+taskType, "")

	logFile := filepath.Join(agentserver.LogsDirectory, taskName+".log")
	f, err := os.Create(logFile)
	if err != nil {
		klog.Errorf("Failed to create log file: %v", err)
		tr.updateTaskStatus(ctx, task, "Failed", fmt.Sprintf("failed to create log file: %v", err))
		return
	}
	defer f.Close()

	var cmd *exec.Cmd

	switch taskType {
	case "review":
		cmd = exec.Command("/opt/repo-agent/repo-sandbox", "review")
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
		cmd = exec.Command("/opt/repo-agent/repo-sandbox", "github-fix-issue", "--in-pod=true")
		// Map params to env vars
		cmd.Env = os.Environ()
		// Inject params into env
		for k, v := range params {
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", strings.ToUpper(k), v))
		}

	case "address-feedback":
		cmd = exec.Command("/opt/repo-agent/repo-sandbox", "github-feedback", "--in-pod=true")
		// Map params to env vars
		cmd.Env = os.Environ()
		// Inject params into env
		for k, v := range params {
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", strings.ToUpper(k), v))
		}

	case "triage-issue":
		cmd = exec.Command("/opt/repo-agent/repo-sandbox", "github-triage-issue", "--in-pod=true")
		// Map params to env vars
		cmd.Env = os.Environ()
		// Inject params into env
		for k, v := range params {
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", strings.ToUpper(k), v))
		}

	case "issue":
		cmd = exec.Command("/opt/repo-agent/repo-sandbox", "dev")
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
			tr.updateTaskStatus(ctx, task, "Failed", "missing 'command' param")
			return
		}

	default:
		klog.Warningf("Unknown task type: %s", taskType)
		tr.updateTaskStatus(ctx, task, "Failed", "unknown task type")
		return
	}

	taskDir, err := tr.createTaskDir(taskName)
	if err != nil {
		tr.updateTaskStatus(ctx, task, "Failed", err.Error())
		return
	}
	cmd.Env = append(cmd.Env, fmt.Sprintf("NAME=%s", taskName))
	cmd.Env = append(cmd.Env, fmt.Sprintf("TASKDIR=%s", taskDir))
	cmd.Stdout = f
	cmd.Stderr = f

	klog.Infof("Starting command for task %s", taskName)
	if err := cmd.Start(); err != nil {
		klog.Errorf("Failed to start command: %v", err)
		tr.updateTaskStatus(ctx, task, "Failed", err.Error())
		return
	}

	if err := cmd.Wait(); err != nil {
		klog.Errorf("Command failed: %v", err)
		tr.updateTaskStatus(ctx, task, "Failed", err.Error())
		_ = tr.ao.SetAgentState(ctx, "Failed "+taskType, err.Error())
	} else {
		klog.Infof("Task %s completed successfully", taskName)
		tr.updateTaskStatus(ctx, task, "Completed", "")
		// Set sandbox state to Running Task
		_ = tr.ao.SetAgentState(ctx, taskType+" Ready", "")
	}
}

func (tr *TaskRunner) updateTaskStatus(ctx context.Context, task *sandboxtaskv1alpha1.SandboxTask, state, result string) {
	if err := tr.manager.UpdateSandboxTaskStatus(ctx, tr.namespace, task.GetName(), state, result); err != nil {
		klog.Errorf("Failed to update task status: %v", err)
	}
}
