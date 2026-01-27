package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/clients"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/klog/v2"
)

// Executor defines the interface for executing commands and managing files.
// It abstracts away the difference between running in a pod and running locally.
type Executor interface {
	Exec(opts ExecOptions) error
	WriteFile(path string, data []byte) error
	ReadFile(path string) ([]byte, error)
	ID() string
}

// ExecOptions holds options for executing a command.
type ExecOptions struct {
	Command []string
	Secrets []string
	Stdin   []byte
	Stdout  io.Writer
	Stderr  io.Writer
	Env     map[string]string
}

// PodExecutor implements Executor for running commands in a Kubernetes pod.
type PodExecutor struct {
	Ctx   context.Context
	Kube  *clients.KubernetesClient
	PodID types.NamespacedName
}

func (e *PodExecutor) Exec(opts ExecOptions) error {
	return ExecInPod(e.Ctx, e.Kube, e.PodID, opts)
}

func (e *PodExecutor) ID() string {
	return fmt.Sprintf("%s/%s", e.PodID.Namespace, e.PodID.Name)
}

func (e *PodExecutor) WriteFile(path string, data []byte) error {
	return WriteFileInPod(e.Ctx, e.Kube, e.PodID, path, data)
}

func (e *PodExecutor) ReadFile(path string) ([]byte, error) {
	var stdout bytes.Buffer
	opts := ExecOptions{
		Command: []string{"cat", path},
		Stdout:  &stdout,
	}
	if err := e.Exec(opts); err != nil {
		return nil, fmt.Errorf("reading file %q in pod: %w", path, err)
	}
	return stdout.Bytes(), nil
}

// LocalExecutor implements Executor for running commands locally.
type LocalExecutor struct {
	Ctx     context.Context
	WorkDir string
	Name    string
}

func (e *LocalExecutor) ID() string {
	return e.Name
}

func (e *LocalExecutor) Exec(opts ExecOptions) error {
	log := klog.FromContext(e.Ctx)

	name := opts.Command[0]
	args := opts.Command[1:]

	cmd := exec.CommandContext(e.Ctx, name, args...)
	if e.WorkDir != "" {
		cmd.Dir = e.WorkDir
	}

	if len(opts.Env) > 0 {
		env := os.Environ()
		for k, v := range opts.Env {
			env = append(env, fmt.Sprintf("%s=%s", k, v))
		}
		cmd.Env = env
	}
	if opts.Stdin != nil {
		cmd.Stdin = bytes.NewReader(opts.Stdin)
	}
	if opts.Stdout != nil {
		cmd.Stdout = opts.Stdout
	} else {
		cmd.Stdout = os.Stdout
	}
	if opts.Stderr != nil {
		cmd.Stderr = opts.Stderr
	} else {
		cmd.Stderr = os.Stderr
	}

	// Environment? We might want to inherit or set.
	cmd.Env = os.Environ()
	// Filter secrets from logging?
	redactedCommand := strings.Join(opts.Command, " ")
	for _, v := range opts.Secrets {
		redactedCommand = strings.ReplaceAll(redactedCommand, v, "****")
	}

	log.Info("Executing local command", "command", redactedCommand, "dir", cmd.Dir)

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("executing local command %q: %w", redactedCommand, err)
	}
	return nil
}

func (e *LocalExecutor) WriteFile(path string, data []byte) error {
	// If path is absolute, use it? Or relative to WorkDir?
	// The paths in our code are often absolute container paths like /workspaces/...
	// For local execution, we might need to map these or just assume user knows what they are doing.
	// For now, let's just write to the path.

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating directory %q: %w", dir, err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("writing file %q: %w", path, err)
	}
	return nil
}

func (e *LocalExecutor) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

// ExecInPod writes the specified data to a file in the specified pod.
func ExecInPod(ctx context.Context, kube *clients.KubernetesClient, podID types.NamespacedName, opts ExecOptions) error {
	log := klog.FromContext(ctx)

	redactedCommand := strings.Join(opts.Command, " ")
	for _, v := range opts.Secrets {
		redactedCommand = strings.ReplaceAll(redactedCommand, v, "****")
	}

	command := opts.Command
	if len(opts.Env) > 0 {
		envCommands := []string{}
		for k, v := range opts.Env {
			envCommands = append(envCommands, fmt.Sprintf("export %s=%q", k, v))
		}
		// Prepend the env commands to the original command
		shellCommand := strings.Join(envCommands, " && ") + " && " + strings.Join(command, " ")
		command = []string{"sh", "-c", shellCommand}
	}

	log.Info("Executing command in pod", "pod", podID, "command", redactedCommand)

	podExecOptions := &v1.PodExecOptions{
		// Container: containerName,
		Command: command,
		Stdin:   true,
		Stdout:  true,
		Stderr:  true,
		TTY:     false,
	}
	if opts.Stdin == nil {
		podExecOptions.Stdin = false
	}
	req := kube.Clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podID.Name).
		Namespace(podID.Namespace).
		SubResource("exec").
		VersionedParams(podExecOptions, scheme.ParameterCodec)

	url := req.URL().String()
	exec, err := remotecommand.NewWebSocketExecutor(kube.RestConfig, "POST", url)
	if err != nil {
		return fmt.Errorf("executing command in pod: %w", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	streamOptions := remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
		Tty:    false,
	}
	if opts.Stdin != nil {
		streamOptions.Stdin = bytes.NewReader(opts.Stdin)
	}
	if opts.Stdout != nil {
		streamOptions.Stdout = opts.Stdout
	}
	if opts.Stderr != nil {
		streamOptions.Stderr = opts.Stderr
	}

	// Run the command
	if err := exec.StreamWithContext(ctx, streamOptions); err != nil {
		log.Error(err, "executing command", "pod", podID, "command", redactedCommand, "stdout", stdout.String(), "stderr", stderr.String())
		return fmt.Errorf("streaming command in pod: %w", err)
	}

	log.Info("executed command", "pod", podID, "command", redactedCommand, "stdout", stdout.String(), "stderr", stderr.String())
	return nil
}

// WriteFileInPod writes the specified data to a file in the specified pod.
func WriteFileInPod(ctx context.Context, kube *clients.KubernetesClient, podID types.NamespacedName, path string, data []byte) error {
	// log := klog.FromContext(ctx)

	var stdout bytes.Buffer

	opt := ExecOptions{
		Command: []string{"/bin/tee", path},
		Stdin:   data,
		Stdout:  &stdout, // To avoid logging to stdout
	}

	return ExecInPod(ctx, kube, podID, opt)
}

// WaitForPodReady waits for the specified pod to be ready.
func WaitForPodReady(ctx context.Context, kube *clients.KubernetesClient, podID types.NamespacedName) error {
	log := klog.FromContext(ctx)

	clientset := kube.Clientset

	log.Info("Waiting for sandbox pod to be ready", "pod", podID.Name)

	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	for {
		stream, err := clientset.CoreV1().Pods(podID.Namespace).Watch(ctx, metav1.ListOptions{FieldSelector: "metadata.name=" + podID.Name, Watch: true})
		if err != nil {
			return err
		}
		defer stream.Stop()
		for event := range stream.ResultChan() {
			pod, ok := event.Object.(*v1.Pod)
			if !ok {
				return fmt.Errorf("unexpected type %T when watching pod", event.Object)
			}
			if isPodReady(pod) {
				log.Info("Sandbox pod is ready", "pod", podID.Name)
				return nil
			}
		}
	}
}

func isPodReady(pod *v1.Pod) bool {
	for _, cond := range pod.Status.Conditions {
		if cond.Type == v1.PodReady {
			return cond.Status == v1.ConditionTrue
		}
	}
	return false
}
