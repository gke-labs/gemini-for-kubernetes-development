package commands

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

// CodebotSandbox represents an agent sandbox being used to fix a GitHub issue.
type CodebotSandbox struct {
	kube  *clients.KubernetesClient
	podID types.NamespacedName
	repo  *github.Repo
	issue *github.Issue
}

func (s *CodebotSandbox) MkdirAll(ctx context.Context, path string) error {
	opts := execOptions{
		Command: []string{"mkdir", "-p", path},
	}
	if err := execInPod(ctx, s.kube, s.podID, opts); err != nil {
		return fmt.Errorf("creating directory %q in pod: %w", path, err)
	}
	return nil
}

func (s *CodebotSandbox) WriteFile(ctx context.Context, path string, data []byte) error {
	if err := writeFileInPod(ctx, s.kube, s.podID, path, data); err != nil {
		return fmt.Errorf("writing file %q in pod: %w", path, err)
	}
	return nil
}

func launchSandboxForIssue(ctx context.Context, kube *clients.KubernetesClient, repo *github.Repo, issue *github.Issue) (*CodebotSandbox, error) {
	log := klog.FromContext(ctx)

	sandboxName := sandboxNameForIssue(repo, issue)

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

	if err := waitForPodReady(ctx, kube, podID); err != nil {
		return nil, err
	}

	return &CodebotSandbox{
		kube:  kube,
		podID: podID,
		repo:  repo,
		issue: issue,
	}, nil
}

func sandboxNameForIssue(repo *github.Repo, issue *github.Issue) string {
	sandboxName := fmt.Sprintf("github-%s-%s-%d", repo.Owner, repo.Name, issue.IssueNumber)
	sandboxName = strings.ToLower(sandboxName) // Repos can have capital letters, but k8s names must be lowercase

	return sandboxName
}

func findSandboxForIssue(ctx context.Context, kube *clients.KubernetesClient, repo *github.Repo, issue *github.Issue) (*CodebotSandbox, bool, error) {
	sandboxName := sandboxNameForIssue(repo, issue)

	podIDPtr, err := findSandboxPod(ctx, sandboxName)
	if err != nil {
		return nil, false, err
	}

	if podIDPtr == nil {
		return nil, false, nil
	}

	return &CodebotSandbox{
		kube:  kube,
		podID: *podIDPtr,
		repo:  repo,
		issue: issue,
	}, true, nil
}

func (s *CodebotSandbox) ReadFile(ctx context.Context, path string) ([]byte, error) {
	var stdout bytes.Buffer

	opt := execOptions{
		Command: []string{"cat", path},
		Stdout:  &stdout,
	}

	if err := execInPod(ctx, s.kube, s.podID, opt); err != nil {
		return nil, fmt.Errorf("reading file %q in pod: %w", path, err)
	}

	return stdout.Bytes(), nil
}

func (s *CodebotSandbox) setupGit(ctx context.Context) error {
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

		opts := execOptions{
			Command: []string{"mkdir", "-p", "/root/.config/gh"},
		}
		if err := execInPod(ctx, s.kube, s.podID, opts); err != nil {
			return fmt.Errorf("creating /root/.config/gh directory: %w", err)
		}

		if err := writeFileInPod(ctx, s.kube, s.podID, "/root/.config/gh/hosts.yml", []byte(config)); err != nil {
			return fmt.Errorf("writing gh config into pod: %w", err)
		}
	}

	// Run git config
	{
		opts := execOptions{
			Command: []string{"git", "config", "--global", "user.email", "codebot-robot@google.com"},
		}
		if err := execInPod(ctx, s.kube, s.podID, opts); err != nil {
			return fmt.Errorf("running git config user.email in pod: %w", err)
		}
		opts = execOptions{
			Command: []string{"git", "config", "--global", "user.name", "codebot-robot"},
		}
		if err := execInPod(ctx, s.kube, s.podID, opts); err != nil {
			return fmt.Errorf("running git config user.name in pod: %w", err)
		}
	}

	// Run gh auth setup-git
	{
		opts := execOptions{
			Command: []string{"gh", "auth", "setup-git"},
		}
		if err := execInPod(ctx, s.kube, s.podID, opts); err != nil {
			return fmt.Errorf("running gh auth setup-git in pod: %w", err)
		}
	}

	return nil
}

func (s *CodebotSandbox) SetupGitRepos(ctx context.Context) error {
	log := klog.FromContext(ctx)

	workdir := fmt.Sprintf("/workspaces/%s", s.repo.FilesystemName())

	// Run gh repo fork
	log.Info("Forking repository in pod", "pod", s.podID.Name, "repo", s.repo.GitCloneURL())
	{
		// TODO: Does gh support -C ?
		opts := execOptions{
			Command: []string{"sh", "-c", fmt.Sprintf("cd %s && gh repo fork --remote", workdir)},
		}
		if err := execInPod(ctx, s.kube, s.podID, opts); err != nil {
			return fmt.Errorf("running gh repo fork in pod: %w", err)
		}
	}

	// Setup default remote
	{
		defaultRepo := s.repo.GitCloneURL()

		// TODO: Does gh support -C ?
		opts := execOptions{
			Command: []string{"sh", "-c", fmt.Sprintf("cd %s && gh repo set-default %s", workdir, defaultRepo)},
		}
		if err := execInPod(ctx, s.kube, s.podID, opts); err != nil {
			return fmt.Errorf("running gh repo fork in pod: %w", err)
		}

	}

	// Wait for checkout to complete
	{
		timeoutAt := time.Now().Add(time.Minute)
		for {
			log.Info("Waiting for checkout to be ready")

			var stdout bytes.Buffer
			opts := execOptions{
				Command: []string{"git", "-C", workdir, "branch", "--show-current"},
				Stdout:  &stdout,
			}
			if err := execInPod(ctx, s.kube, s.podID, opts); err != nil {
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

func (s *CodebotSandbox) CheckoutNewBranch(ctx context.Context) error {
	log := klog.FromContext(ctx)

	workdir := fmt.Sprintf("/workspaces/%s", s.repo.FilesystemName())

	branchName := fmt.Sprintf("issue_%d", s.issue.IssueNumber)

	// Create a new branch
	log.Info("Creating new branch in pod", "pod", s.podID.Name, "branch", branchName)

	opts := execOptions{
		Command: []string{"git", "-C", workdir, "checkout", "-b", branchName},
	}
	if err := execInPod(ctx, s.kube, s.podID, opts); err != nil {
		return fmt.Errorf("creating new branch in pod: %w", err)
	}

	return nil
}

func (s *CodebotSandbox) CheckoutExistingBranch(ctx context.Context, branchName string) error {
	log := klog.FromContext(ctx)

	workdir := fmt.Sprintf("/workspaces/%s", s.repo.FilesystemName())

	log.Info("Fetching from fork in pod", "pod", s.podID.Name)

	opts := execOptions{
		Command: []string{"git", "-C", workdir, "fetch", "origin"},
	}
	if err := execInPod(ctx, s.kube, s.podID, opts); err != nil {
		return fmt.Errorf("fetching from fork in pod: %w", err)
	}

	opts = execOptions{
		Command: []string{"git", "-C", workdir, "checkout", branchName},
	}
	if err := execInPod(ctx, s.kube, s.podID, opts); err != nil {
		return fmt.Errorf("checking out branch %q in pod: %w", branchName, err)
	}

	return nil
}

func (s *CodebotSandbox) ListThreads(ctx context.Context) ([]ThreadInfo, error) {
	threads, err := listThreads(ctx, s.podID)
	if err != nil {
		return nil, fmt.Errorf("failed to list threads: %w", err)
	}
	return threads, nil
}

func (s *CodebotSandbox) GetThreadMessages(ctx context.Context, threadID string) ([]ThreadMessage, error) {
	getThreadOptions := GetThreadsOptions{
		ThreadID:        threadID,
		IncludeMessages: true,
	}
	thread, err := getThread(ctx, s.podID, getThreadOptions)
	if err != nil {
		return nil, fmt.Errorf("failed to get thread %q: %w", threadID, err)
	}
	return thread.Messages, nil
}

func configureGemini(ctx context.Context, sandbox *CodebotSandbox) error {
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
