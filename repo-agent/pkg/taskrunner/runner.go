package taskrunner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/agentoutput"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/agentserver"
	k8s_metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8s_types "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
)

var (
	SandboxTaskGVR = schema.GroupVersionResource{
		Group:    "custom.agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "sandboxtasks",
	}
)

type TaskRunner struct {
	client      dynamic.Interface
	namespace   string
	sandboxName string
	ao          *agentoutput.AgentOutput
}

func NewTaskRunner(ao *agentoutput.AgentOutput) (*TaskRunner, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get in-cluster config: %w", err)
	}

	client, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create dynamic client: %w", err)
	}

	ns := os.Getenv("NAMESPACE")
	name := os.Getenv("NAME")

	if ns == "" || name == "" {
		return nil, fmt.Errorf("NAMESPACE and NAME environment variables must be set")
	}

	return &TaskRunner{
		client:      client,
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

	// List tasks with label selector
	listOptions := k8s_metav1.ListOptions{
		LabelSelector: fmt.Sprintf("sandbox.gemini.google.com/sandbox-name=%s", tr.sandboxName),
	}

	tasks, err := tr.client.Resource(SandboxTaskGVR).Namespace(tr.namespace).List(ctx, listOptions)
	if err != nil {
		klog.Errorf("Failed to list tasks: %v", err)
		return
	}

	for _, task := range tasks.Items {
		taskState, _, _ := unstructured.NestedString(task.Object, "status", "taskState")
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
func (tr *TaskRunner) executeTask(ctx context.Context, task *unstructured.Unstructured) {
	taskName := task.GetName()
	klog.Infof("Processing task: %s", taskName)

	// Update status to Running
	tr.updateTaskStatus(ctx, task, "Running", "")

	taskType, _, _ := unstructured.NestedString(task.Object, "spec", "type")
	params, _, _ := unstructured.NestedStringMap(task.Object, "spec", "params")

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

func (tr *TaskRunner) updateTaskStatus(ctx context.Context, task *unstructured.Unstructured, state, result string) {
	klog.Infof("Updating task %s status to %s", task.GetName(), state)
	status := map[string]interface{}{
		"status": map[string]interface{}{
			"taskState": state,
			"result":    result,
		},
	}

	patchBytes, _ := json.Marshal(status)

	_, err := tr.client.Resource(SandboxTaskGVR).Namespace(tr.namespace).Patch(ctx, task.GetName(), k8s_types.MergePatchType, patchBytes, k8s_metav1.PatchOptions{}, "status")
	if err != nil {
		klog.Errorf("Failed to update task status: %v", err)
	}
	klog.Infof("Task %s status updated to %s", task.GetName(), state)
}
