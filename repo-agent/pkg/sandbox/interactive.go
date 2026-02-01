package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

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

// IssueSandbox represents an agent sandbox being used to fix a GitHub issue.
type IssueSandbox struct {
	repo     *github.Repository
	issue    *github.Issue
	executor Executor
}

func NewIssueSandbox(ctx context.Context, local bool, repo *github.Repository, issue *github.Issue) (*IssueSandbox, error) {
	log := klog.FromContext(ctx)

	kube, err := clients.NewKubernetesClient()
	if err != nil {
		return nil, err
	}

	if local {
		log.Info("Using local executor for sandbox")
		return &IssueSandbox{
			repo:  repo,
			issue: issue,
			executor: &LocalExecutor{
				Ctx:  ctx,
				Name: fmt.Sprintf("local-%s/issue/%d", repo.Name(), issue.Number()),
			},
		}, nil
	}
	log.Info("Looking for existing sandbox for issue", "repo", repo.CloneURL(), "issue", issue.String())
	sb, found, err := FindSandboxForIssue(ctx, kube, repo, issue)
	if err != nil {
		return nil, err
	}

	if !found {
		sb, err = LaunchSandboxForIssue(ctx, kube, repo, issue)
		if err != nil {
			return nil, fmt.Errorf("launching sandbox for issue: %w", err)
		}
	}
	return sb, nil
}

// NewSandboxFromPodID creates an IssueSandbox from a specific pod ID.
// This is useful when reusing an existing sandbox.
func NewSandboxFromPodID(ctx context.Context, podID types.NamespacedName) (*IssueSandbox, error) {
	kube, err := clients.NewKubernetesClient()
	if err != nil {
		return nil, err
	}
	return &IssueSandbox{
		executor: &PodExecutor{
			Ctx:   ctx,
			Kube:  kube,
			PodID: podID,
		},
	}, nil
}

// GetPodID returns the pod ID of the sandbox if it is running in a pod.
func (s *IssueSandbox) GetPodID() types.NamespacedName {
	if podExecutor, ok := s.executor.(*PodExecutor); ok {
		return podExecutor.PodID
	}
	return types.NamespacedName{}
}

// GetIssue returns the issue associated with the sandbox.
func (s *IssueSandbox) GetIssue() *github.Issue {
	return s.issue
}

func (s *IssueSandbox) GetSandboxID() string {
	return s.executor.ID()
}

func (s *IssueSandbox) Exec(opts ExecOptions) error {
	return s.executor.Exec(opts)
}

func (s *IssueSandbox) MkdirAll(path string) error {
	opts := ExecOptions{
		Command: []string{"mkdir", "-p", path},
	}
	if err := s.executor.Exec(opts); err != nil {
		return fmt.Errorf("creating directory %q: %w", path, err)
	}
	return nil
}

func (s *IssueSandbox) WriteFile(path string, data []byte) error {
	if err := s.executor.WriteFile(path, data); err != nil {
		return fmt.Errorf("writing file %q: %w", path, err)
	}
	return nil
}

func (s *IssueSandbox) WriteXFile(path string, data []byte) error {
	if err := s.executor.WriteXFile(path, data); err != nil {
		return fmt.Errorf("writing executable script %q: %w", path, err)
	}
	return nil
}

func LaunchSandboxForIssue(ctx context.Context, kube *clients.KubernetesClient, repo *github.Repository, issue *github.Issue) (*IssueSandbox, error) {
	log := klog.FromContext(ctx)

	sandboxName := NameForIssue(repo, issue)

	issueURL := issue.String()

	cloneRepos := []string{
		fmt.Sprintf("/workspaces/%s=%s", repo.Name(), repo.CloneURL()),
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

	return &IssueSandbox{
		repo:  repo,
		issue: issue,
		executor: &PodExecutor{
			Ctx:   ctx,
			Kube:  kube,
			PodID: podID,
		},
	}, nil
}

func NameForIssue(repo *github.Repository, issue *github.Issue) string {
	sandboxName := fmt.Sprintf("github-%s-%s-%d", repo.Owner(), repo.Name(), issue.Number())
	sandboxName = strings.ToLower(sandboxName) // Repos can have capital letters, but k8s names must be lowercase

	return sandboxName
}

func FindSandboxForIssue(ctx context.Context, kube *clients.KubernetesClient, repo *github.Repository, issue *github.Issue) (*IssueSandbox, bool, error) {
	sandboxName := NameForIssue(repo, issue)

	podIDPtr, err := FindSandboxPod(ctx, sandboxName)
	if err != nil {
		return nil, false, err
	}

	if podIDPtr == nil {
		return nil, false, nil
	}

	return &IssueSandbox{
		repo:  repo,
		issue: issue,
		executor: &PodExecutor{
			Ctx:   ctx,
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

func (s *IssueSandbox) ReadFile(path string) ([]byte, error) {
	return s.executor.ReadFile(path)
}

func (s *IssueSandbox) ListThreads() ([]ThreadInfo, error) {
	return ListThreads(s.executor)
}

func ListThreads(executor Executor) ([]ThreadInfo, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	opts := ExecOptions{
		Command: []string{RepoSandboxBinary, "threads", "agent"},
		Stdout:  &stdout,
		Stderr:  &stderr,
	}

	if err := executor.Exec(opts); err != nil {
		return nil, fmt.Errorf("failed to list threads via agent: %w, stderr: %s", err, stderr.String())
	}

	var threads []ThreadInfo
	if err := json.Unmarshal(stdout.Bytes(), &threads); err != nil {
		return nil, fmt.Errorf("failed to parse threads agent output: %w", err)
	}
	return threads, nil
}

func (s *IssueSandbox) GetThreadMessages(threadID string) ([]ThreadMessage, error) {
	return GetThreadMessages(s.executor, threadID)
}

func GetThread(executor Executor, threadID string, includeMessages bool) (*ThreadInfo, error) {
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

	if err := executor.Exec(opts); err != nil {
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

func GetThreadMessages(executor Executor, threadID string) ([]ThreadMessage, error) {
	thread, err := GetThread(executor, threadID, true)
	if err != nil {
		return nil, err
	}
	return thread.Messages, nil
}

func (s *IssueSandbox) ConfigureGemini(ctx context.Context) error {
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

		log.Info("Writing gemini config in pod", "sandbox", s.GetSandboxID())

		// if b0, err := sandbox.ReadFile(ctx, "/root/.gemini/settings.json"); err != nil {
		// 	return fmt.Errorf("reading gemini config in pod: %w", err)
		// } else {
		// 	klog.Infof("Existing gemini config: %s", string(b0))
		// }

		if err := s.MkdirAll("/root/.gemini"); err != nil {
			return fmt.Errorf("creating /root/.gemini directory in pod: %w", err)
		}

		if err := s.WriteFile("/root/.gemini/settings.json", b); err != nil {
			return fmt.Errorf("writing gemini config in pod: %w", err)
		}
	}
	return nil
}
