package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/github"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"

	sandboxapi "sigs.k8s.io/agent-sandbox/api/v1alpha1"
)

const RepoSandboxBinary = "/repo-agent/repo-sandbox"

// InteractiveSandbox represents an agent sandbox being used to fix a GitHub issue.
type InteractiveSandbox struct {
	kube     *clients.KubernetesClient
	podID    types.NamespacedName
	repo     *github.Repo
	issue    *github.Issue
	executor Executor
}

// GetPodID returns the pod ID of the sandbox.
func (s *InteractiveSandbox) GetPodID() types.NamespacedName {
	return s.podID
}

// GetRepo returns the repo associated with the sandbox.
func (s *InteractiveSandbox) GetRepo() *github.Repo {
	return s.repo
}

func (s *InteractiveSandbox) Exec(ctx context.Context, opts ExecOptions) error {
	return s.executor.Exec(ctx, opts)
}

func (s *InteractiveSandbox) MkdirAll(ctx context.Context, path string) error {
	opts := ExecOptions{
		Command: []string{"mkdir", "-p", path},
	}
	if err := s.executor.Exec(ctx, opts); err != nil {
		return fmt.Errorf("creating directory %q: %w", path, err)
	}
	return nil
}

func (s *InteractiveSandbox) WriteFile(ctx context.Context, path string, data []byte) error {
	if err := s.executor.WriteFile(ctx, path, data); err != nil {
		return fmt.Errorf("writing file %q: %w", path, err)
	}
	return nil
}

func LaunchSandboxForIssue(ctx context.Context, kube *clients.KubernetesClient, repo *github.Repo, issue *github.Issue) (*InteractiveSandbox, error) {
	log := klog.FromContext(ctx)

	sandboxName := NameForIssue(repo, issue)

	issueURL := issue.String()

	cloneRepos := []string{
		fmt.Sprintf("/workspaces/%s=%s", repo.FilesystemName(), repo.GitCloneURL()),
	}

	log.Info("Creating sandbox", "name", sandboxName, "repos", cloneRepos, "issue", issueURL)

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
		// This enables FindSandboxPod to work, even if we are launching the dev sandbox directly
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
		return nil, err
	}
	u := &unstructured.Unstructured{Object: uObj}
	_, err = kube.DynamicClient.Resource(sandboxGVR).Namespace(sandbox.Namespace).Create(ctx, u, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to create sandbox: %w", err)
	}

	log.Info("Sandbox created", "name", sandboxName)

	podID := types.NamespacedName{
		Namespace: kube.CurrentNamespace,
		Name:      sandboxName,
	}

	if err := WaitForPodReady(ctx, kube, podID); err != nil {
		return nil, err
	}

	return &InteractiveSandbox{
		kube:  kube,
		podID: podID,
		repo:  repo,
		issue: issue,
		executor: &PodExecutor{
			Kube:  kube,
			PodID: podID,
		},
	}, nil
}

func NameForIssue(repo *github.Repo, issue *github.Issue) string {
	sandboxName := fmt.Sprintf("github-%s-%s-%d", repo.Owner, repo.Name, issue.IssueNumber)
	sandboxName = strings.ToLower(sandboxName) // Repos can have capital letters, but k8s names must be lowercase

	return sandboxName
}

func FindSandboxForIssue(ctx context.Context, kube *clients.KubernetesClient, repo *github.Repo, issue *github.Issue) (*InteractiveSandbox, bool, error) {
	sandboxName := NameForIssue(repo, issue)

	podIDPtr, err := FindSandboxPod(ctx, sandboxName)
	if err != nil {
		return nil, false, err
	}

	if podIDPtr == nil {
		return nil, false, nil
	}

	return &InteractiveSandbox{
		kube:  kube,
		podID: *podIDPtr,
		repo:  repo,
		issue: issue,
		executor: &PodExecutor{
			Kube:  kube,
			PodID: *podIDPtr,
		},
	}, true, nil
}

// FindSandboxPod finds the pod for the given sandbox name.
// If the pod is not found, it returns (nil, nil)
func FindSandboxPod(ctx context.Context, sandboxName string) (*types.NamespacedName, error) {
	kube, err := clients.NewKubernetesClient()
	if err != nil {
		return nil, err
	}

	clientset := kube.Clientset
	namespace := kube.CurrentNamespace

	// The sandbox name in the RGD is devc-<name>
	// And the pods have label sandbox=devc-<name>
	labelSelector := fmt.Sprintf("sandbox=devc-%s", sandboxName)
	pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list pods with selector %q: %w", labelSelector, err)
	}

	if len(pods.Items) == 0 {
		return nil, nil
	}

	// Pick the first running pod, or just the first one if none are running yet (though exec will fail)
	pod := &pods.Items[0]

	for _, p := range pods.Items {
		if p.Status.Phase == "Running" {
			pod = &p
			break
		}
	}

	podID := &types.NamespacedName{
		Name:      pod.Name,
		Namespace: pod.Namespace,
	}
	return podID, nil
}

func (s *InteractiveSandbox) ReadFile(ctx context.Context, path string) ([]byte, error) {
	return s.executor.ReadFile(ctx, path)
}

func (s *InteractiveSandbox) SetupGit(ctx context.Context) error {
	// log := klog.FromContext(ctx)

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

		codebotRobotToken := os.Getenv("CODEBOT_ROBOT_GITHUB_TOKEN")
		if codebotRobotToken == "" {
			return fmt.Errorf("CODEBOT_ROBOT_GITHUB_TOKEN environment variable is not set")
		}

		config = strings.ReplaceAll(config, "{{CODEBOT_ROBOT_GITHUB_TOKEN}}", codebotRobotToken)

		opts := ExecOptions{
			Command: []string{"mkdir", "-p", "/root/.config/gh"},
		}
		if err := s.executor.Exec(ctx, opts); err != nil {
			return fmt.Errorf("creating /root/.config/gh directory: %w", err)
		}

		if err := s.executor.WriteFile(ctx, "/root/.config/gh/hosts.yml", []byte(config)); err != nil {
			return fmt.Errorf("writing gh config: %w", err)
		}
	}

	// Run git config
	{
		opts := ExecOptions{
			Command: []string{"git", "config", "--global", "user.email", "codebot-robot@google.com"},
		}
		if err := s.executor.Exec(ctx, opts); err != nil {
			return fmt.Errorf("running git config user.email: %w", err)
		}
		opts = ExecOptions{
			Command: []string{"git", "config", "--global", "user.name", "codebot-robot"},
		}
		if err := s.executor.Exec(ctx, opts); err != nil {
			return fmt.Errorf("running git config user.name: %w", err)
		}
	}

	// Run gh auth setup-git
	{
		opts := ExecOptions{
			Command: []string{"gh", "auth", "setup-git"},
		}
		if err := s.executor.Exec(ctx, opts); err != nil {
			return fmt.Errorf("running gh auth setup-git: %w", err)
		}
	}

	return nil
}

func (s *InteractiveSandbox) SetupGitRepos(ctx context.Context) error {
	log := klog.FromContext(ctx)

	workdir := fmt.Sprintf("/workspaces/%s", s.repo.FilesystemName())

	// Run gh repo fork
	log.Info("Forking repository", "pod", s.podID.Name, "repo", s.repo.GitCloneURL())
	{
		// TODO: Does gh support -C ?
		opts := ExecOptions{
			Command: []string{"sh", "-c", fmt.Sprintf("cd %s && gh repo fork --remote", workdir)},
		}
		if err := s.executor.Exec(ctx, opts); err != nil {
			return fmt.Errorf("running gh repo fork: %w", err)
		}
	}

	// Setup default remote
	{
		defaultRepo := s.repo.GitCloneURL()

		// TODO: Does gh support -C ?
		opts := ExecOptions{
			Command: []string{"sh", "-c", fmt.Sprintf("cd %s && gh repo set-default %s", workdir, defaultRepo)},
		}
		if err := s.executor.Exec(ctx, opts); err != nil {
			return fmt.Errorf("running gh repo fork: %w", err)
		}

	}

	// Wait for checkout to complete
	{
		timeoutAt := time.Now().Add(time.Minute)
		for {
			log.Info("Waiting for checkout to be ready")

			var stdout bytes.Buffer
			opts := ExecOptions{
				Command: []string{"git", "-C", workdir, "branch", "--show-current"},
				Stdout:  &stdout,
			}
			if err := s.executor.Exec(ctx, opts); err != nil {
				klog.Infof("stdout: %v", stdout.String())
				if time.Now().After(timeoutAt) {
					return fmt.Errorf("timed out waiting for initial checkout to complete: %w", err)
				}
			} else {
				klog.Infof("current branch: %v", stdout.String())
				break
			}

			time.Sleep(2 * time.Second)
		}
	}

	return nil
}

func (s *InteractiveSandbox) CheckoutNewBranch(ctx context.Context) error {
	log := klog.FromContext(ctx)

	workdir := fmt.Sprintf("/workspaces/%s", s.repo.FilesystemName())

	branchName := fmt.Sprintf("issue_%d", s.issue.IssueNumber)

	// Create a new branch
	log.Info("Creating new branch", "pod", s.podID.Name, "branch", branchName)

	opts := ExecOptions{
		Command: []string{"git", "-C", workdir, "checkout", "-b", branchName},
	}
	if err := s.executor.Exec(ctx, opts); err != nil {
		return fmt.Errorf("creating new branch: %w", err)
	}

	return nil
}

func (s *InteractiveSandbox) CheckoutExistingBranch(ctx context.Context, branchName string) error {
	log := klog.FromContext(ctx)

	workdir := fmt.Sprintf("/workspaces/%s", s.repo.FilesystemName())

	log.Info("Fetching from fork", "pod", s.podID.Name)

	opts := ExecOptions{
		Command: []string{"git", "-C", workdir, "fetch", "origin"},
	}
	if err := s.executor.Exec(ctx, opts); err != nil {
		return fmt.Errorf("fetching from fork: %w", err)
	}

	opts = ExecOptions{
		Command: []string{"git", "-C", workdir, "checkout", branchName},
	}
	if err := s.executor.Exec(ctx, opts); err != nil {
		return fmt.Errorf("checking out branch %q: %w", branchName, err)
	}

	return nil
}

func (s *InteractiveSandbox) ListThreads(ctx context.Context) ([]ThreadInfo, error) {
	return ListThreads(ctx, s.executor)
}

func ListThreads(ctx context.Context, executor Executor) ([]ThreadInfo, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	opts := ExecOptions{
		Command: []string{RepoSandboxBinary, "threads", "agent"},
		Stdout:  &stdout,
		Stderr:  &stderr,
	}

	if err := executor.Exec(ctx, opts); err != nil {
		return nil, fmt.Errorf("failed to list threads via agent: %w, stderr: %s", err, stderr.String())
	}

	var threads []ThreadInfo
	if err := json.Unmarshal(stdout.Bytes(), &threads); err != nil {
		return nil, fmt.Errorf("failed to parse threads agent output: %w", err)
	}
	return threads, nil
}

func (s *InteractiveSandbox) GetThreadMessages(ctx context.Context, threadID string) ([]ThreadMessage, error) {
	return GetThreadMessages(ctx, s.executor, threadID)
}

func GetThread(ctx context.Context, executor Executor, threadID string, includeMessages bool) (*ThreadInfo, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	args := []string{RepoSandboxBinary, "threads", "agent", fmt.Sprintf("--thread-id=%s", threadID)}
	if includeMessages {
		args = append(args, "--include-messages=true")
	}

	opts := ExecOptions{
		Command: args,
		Stdout:  &stdout,
		Stderr:  &stderr,
	}

	if err := executor.Exec(ctx, opts); err != nil {
		return nil, fmt.Errorf("failed to get thread via agent: %w, stderr: %s", err, stderr.String())
	}

	var threads []ThreadInfo
	if err := json.Unmarshal(stdout.Bytes(), &threads); err != nil {
		return nil, fmt.Errorf("failed to parse threads agent output: %w", err)
	}

	if len(threads) == 0 {
		return nil, fmt.Errorf("thread with ID %q not found", threadID)
	}

	return &threads[0], nil
}

func GetThreadMessages(ctx context.Context, executor Executor, threadID string) ([]ThreadMessage, error) {
	thread, err := GetThread(ctx, executor, threadID, true)
	if err != nil {
		return nil, err
	}
	return thread.Messages, nil
}

func ConfigureGemini(ctx context.Context, sandbox *InteractiveSandbox) error {
	log := klog.FromContext(ctx)

	// Configure gemini
	{
		general := map[string]any{
			"enableAutoUpdate": false,
			"retryFetchErrors": true,
		}

		config := map[string]any{
			"general": general,
		}

		// Maybe:
		// general.checkpointing.enabled
		// output.format
		// general.sessionRetention.enabled (but false is default)
		// model.summarizeToolOutput
		// experimental.enableAgents
		// experimental.plan
		// experimental.codebaseInvestigatorSettings
		// Memory in a shared location?
		// Hooks?
		// telemetry?
		// ui.theme?

		// TODO: Install ripgrep?

		b, err := json.MarshalIndent(config, "", "  ")
		if err != nil {
			return fmt.Errorf("marshaling gemini config: %w", err)
		}

		log.Info("Writing gemini config in pod", "pod", sandbox.podID)

		// if b0, err := sandbox.ReadFile(ctx, "/root/.gemini/settings.json"); err != nil {
		// 	return fmt.Errorf("reading gemini config in pod: %w", err)
		// } else {
		// 	klog.Infof("Existing gemini config: %s", string(b0))
		// }

		if err := sandbox.MkdirAll(ctx, "/root/.gemini"); err != nil {
			return fmt.Errorf("creating /root/.gemini directory in pod: %w", err)
		}

		if err := sandbox.WriteFile(ctx, "/root/.gemini/settings.json", b); err != nil {
			return fmt.Errorf("writing gemini config in pod: %w", err)
		}
	}
	return nil
}
