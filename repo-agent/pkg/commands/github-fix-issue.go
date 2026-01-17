package commands

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/github"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/prompts"
	"github.com/spf13/cobra"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/klog/v2"

	sandboxapi "sigs.k8s.io/agent-sandbox/api/v1alpha1"
)

// GithubFixIssueOptions holds options for the RunCode function.
type GithubFixIssueOptions struct {
	Repo  string
	Issue int
}

// BuildGithubFixIssueCommand creates a new cobra command for using a dev sandbox to solve a github issue
func BuildGithubFixIssueCommand() *cobra.Command {
	var opt GithubFixIssueOptions

	cmd := &cobra.Command{
		Use:   "github-fix-issue",
		Short: "Fix a github issue using an LLM in a dev sandbox",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("command does not take positional arguments")
			}

			return RunGithubFixIssue(cmd.Context(), opt)
		},
	}

	cmd.Flags().StringVar(&opt.Repo, "repo", opt.Repo, "GitHub repository (e.g., gke-labs/gemini-for-kubernetes-development)")
	cmd.Flags().IntVar(&opt.Issue, "issue", opt.Issue, "GitHub issue number")
	return cmd
}

// RunGithubFixIssue launches VS Code connected to the specified dev sandbox.
func RunGithubFixIssue(ctx context.Context, opt GithubFixIssueOptions) error {
	log := klog.FromContext(ctx)

	codebotRobotToken := os.Getenv("CODEBOT_ROBOT_GITHUB_TOKEN")
	if codebotRobotToken == "" {
		return fmt.Errorf("CODEBOT_ROBOT_GITHUB_TOKEN environment variable is not set")
	}

	kube, err := clients.NewKubernetesClient()
	if err != nil {
		return err
	}

	githubAPI, err := github.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("failed to create github client: %w", err)
	}

	if opt.Repo == "" {
		return fmt.Errorf("--repo is required")
	}
	repo, err := github.ParseRepo(opt.Repo)
	if err != nil {
		return err
	}

	if opt.Issue == 0 {
		return fmt.Errorf("--issue is required")
	}
	issueURL := fmt.Sprintf("https://github.com/%s/issues/%d", opt.Repo, opt.Issue)

	cloneRepos := []string{
		fmt.Sprintf("/workspaces/%s=%s", repo.FilesystemName(), repo.GitCloneURL()),
	}

	prompt, err := prompts.FixIssuePrompt(ctx, githubAPI, repo, opt.Issue)
	if err != nil {
		return fmt.Errorf("failed to generate prompt for issue: %w", err)
	}

	sandboxName := fmt.Sprintf("github-%s-%s-%d", repo.Owner, repo.Name, opt.Issue)
	sandboxName = strings.ToLower(sandboxName) // Repos can have capital letters, but k8s names must be lowercase

	// 1. Find the pod
	podID, err := findSandboxPod(ctx, sandboxName)
	if err != nil {
		return err
	}

	if podID == nil {
		log.Info("Creating sandbox", "name", sandboxName, "repos", cloneRepos, "issue", opt.Issue)

		container := v1.Container{}
		container.Name = "agent"
		container.Image = "gcr.io/justinsb-knotai-dev/generic-golang:latest"

		container.Env = append(container.Env, v1.EnvVar{
			Name:  "CLONE_REPOS",
			Value: strings.Join(cloneRepos, ";"),
		})

		sandbox := &sandboxapi.Sandbox{}
		sandbox.Name = sandboxName
		sandbox.Namespace = kube.CurrentNamespace
		sandbox.Spec.PodTemplate.Spec.Containers = append(sandbox.Spec.PodTemplate.Spec.Containers, container)

		sandbox.Spec.PodTemplate.ObjectMeta.Labels = map[string]string{
			// This enables findSandbox to work, even if we are launching the dev sandbox directly
			"sandbox": "devc-" + sandboxName,
		}

		sandbox.Annotations = map[string]string{
			"repo-agent.labs.gke.io/clone-repos": strings.Join(cloneRepos, ";"),
			"repo-agent.labs.gke.io/fix-issue":   issueURL,
		}

		sandboxGVR := sandboxapi.GroupVersion.WithResource("sandboxes")
		sandboxGVK := sandboxapi.GroupVersion.WithKind("Sandbox")

		sandbox.SetGroupVersionKind(sandboxGVK)

		uObj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(sandbox)
		if err != nil {
			return err
		}
		u := &unstructured.Unstructured{Object: uObj}
		_, err = kube.DynamicClient.Resource(sandboxGVR).Namespace(sandbox.Namespace).Create(ctx, u, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("failed to create sandbox: %w", err)
		}

		log.Info("Sandbox created", "name", sandboxName)

		podID = &types.NamespacedName{
			Namespace: kube.CurrentNamespace,
			Name:      sandboxName,
		}
	}

	geminiAPIKey, err := GetGeminiAPIKey(podID.Namespace + "/" + podID.Name)
	if err != nil {
		return err
	}

	if err := waitForPodReady(ctx, kube, podID); err != nil {
		return err
	}

	// Copy the prompt into the pod (for now)
	if len(prompt) > 0 {
		log.Info("copying prompt into sandbox pod", "pod", podID.Name)

		path := "/workspaces/prompt.txt"
		if err := writeFileInPod(ctx, kube, podID, path, prompt); err != nil {
			return fmt.Errorf("copying prompt into sandbox pod: %w", err)
		}

		log.Info("Copied prompt into sandbox pod", "pod", podID.Name, "path", path)
	}

	// mkdir -p ~/.config/gh

	// Write gh config
	{
		config := `github.com:
    users:
        codebot-robot:
            oauth_token: {{CODEBOT_ROBOT_GITHUB_TOKEN}}
    git_protocol: https
    oauth_token: {{CODEBOT_ROBOT_GITHUB_TOKEN}}
    user: codebot-robot
`

		config = strings.ReplaceAll(config, "{{CODEBOT_ROBOT_GITHUB_TOKEN}}", codebotRobotToken)

		opts := execOptions{
			Command: []string{"mkdir", "-p", "/root/.config/gh"},
		}
		if err := execInPod(ctx, kube, podID, opts); err != nil {
			return fmt.Errorf("creating /root/.config/gh directory: %w", err)
		}

		if err := writeFileInPod(ctx, kube, podID, "/root/.config/gh/hosts.yml", []byte(config)); err != nil {
			return fmt.Errorf("writing gh config into pod: %w", err)
		}
	}

	// Run git config
	{
		opts := execOptions{
			Command: []string{"git", "config", "--global", "user.email", "codebot-robot@google.com"},
		}
		if err := execInPod(ctx, kube, podID, opts); err != nil {
			return fmt.Errorf("running git config user.email in pod: %w", err)
		}
		opts = execOptions{
			Command: []string{"git", "config", "--global", "user.name", "codebot-robot"},
		}
		if err := execInPod(ctx, kube, podID, opts); err != nil {
			return fmt.Errorf("running git config user.name in pod: %w", err)
		}
	}

	// Run gh auth setup-git
	{
		opts := execOptions{
			Command: []string{"gh", "auth", "setup-git"},
		}
		if err := execInPod(ctx, kube, podID, opts); err != nil {
			return fmt.Errorf("running gh auth setup-git in pod: %w", err)
		}
	}

	time.Sleep(2 * time.Second) // TODO: Replace with proper wait for git repo to be cloned

	workdir := fmt.Sprintf("/workspaces/%s", repo.FilesystemName())

	// Run gh repo fork
	log.Info("Forking repository in pod", "pod", podID.Name, "repo", repo.GitCloneURL())
	{
		// TODO: Does gh support -C ?
		opts := execOptions{
			Command: []string{"sh", "-c", fmt.Sprintf("cd %s && gh repo fork --remote", workdir)},
		}
		if err := execInPod(ctx, kube, podID, opts); err != nil {
			return fmt.Errorf("running gh repo fork in pod: %w", err)
		}
	}

	// Setup default remote
	{
		defaultRepo := repo.GitCloneURL()

		// TODO: Does gh support -C ?
		opts := execOptions{
			Command: []string{"sh", "-c", fmt.Sprintf("cd %s && gh repo set-default %s", workdir, defaultRepo)},
		}
		if err := execInPod(ctx, kube, podID, opts); err != nil {
			return fmt.Errorf("running gh repo fork in pod: %w", err)
		}

	}
	// Create a new branch
	{
		branchName := fmt.Sprintf("issue_%d", opt.Issue)
		log.Info("Creating new branch in pod", "pod", podID.Name, "branch", branchName)

		opts := execOptions{
			Command: []string{"git", "-C", workdir, "checkout", "-b", branchName},
		}
		if err := execInPod(ctx, kube, podID, opts); err != nil {
			return fmt.Errorf("creating new branch in pod: %w", err)
		}
	}

	// Run gemini with API key and prompt
	{
		log.Info("Running gemini in pod", "pod", podID.Name)

		// TODO:
		// export GEMINI_TELEMETRY_ENABLED=true
		// export GEMINI_TELEMETRY_OTLP_ENDPOINT=http://otel-portal.otel-system:4317

		opts := execOptions{
			Command: []string{"sh", "-c", fmt.Sprintf("cd %s && export GEMINI_API_KEY=%s && gemini --yolo --model gemini-3-pro-preview < /workspaces/prompt.txt", workdir, geminiAPIKey)},
			Stdout:  os.Stdout,
			Stderr:  os.Stderr,
		}
		opts.Secrets = []string{geminiAPIKey}

		if err := execInPod(ctx, kube, podID, opts); err != nil {
			return fmt.Errorf("running gemini: %w", err)
		}
	}

	return nil
}

type execOptions struct {
	Command []string
	Secrets []string

	Stdin  []byte
	Stdout io.Writer
	Stderr io.Writer
}

// execInPod writes the specified data to a file in the specified pod.
func execInPod(ctx context.Context, kube *clients.KubernetesClient, podID *types.NamespacedName, opts execOptions) error {
	log := klog.FromContext(ctx)

	redactedCommand := strings.Join(opts.Command, " ")
	for _, v := range opts.Secrets {
		redactedCommand = strings.ReplaceAll(redactedCommand, v, "****")
	}

	podExecOptions := &v1.PodExecOptions{
		// Container: containerName,
		Command: opts.Command,
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
		log.Error(err, "executing command", "command", redactedCommand, "stdout", stdout.String(), "stderr", stderr.String())
		return fmt.Errorf("streaming command in pod: %w", err)
	}

	log.Info("executed command", "command", redactedCommand, "stdout", stdout.String(), "stderr", stderr.String())

	return nil
}

// writeFileInPod writes the specified data to a file in the specified pod.
func writeFileInPod(ctx context.Context, kube *clients.KubernetesClient, podID *types.NamespacedName, path string, data []byte) error {
	// log := klog.FromContext(ctx)

	var stdout bytes.Buffer

	opt := execOptions{
		Command: []string{"/bin/tee", path},
		Stdin:   data,
		Stdout:  &stdout, // To avoid logging to stdout
	}

	return execInPod(ctx, kube, podID, opt)
}

// waitForPodReady waits for the specified pod to be ready.
func waitForPodReady(ctx context.Context, kube *clients.KubernetesClient, podID *types.NamespacedName) error {
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
