/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math/rand"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kvalidation "k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/yaml"

	overseerv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/overseer/pkg/api/v1alpha1"
	sandboxtaskv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/sandboxtask/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/github"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/models"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/sandbox"
	githubv39 "github.com/google/go-github/v39/github"
)

var (
	overseerName     string
	namespace        string
	dryRun           bool
	IssueModelsOrder = []string{
		"gemini-3-flash-preview",
		"gemini-3.1-pro-preview",
		"gemini-2.5-pro",
		"gemini-2.5-flash",
	}
)

func main() {
	klog.InitFlags(nil)

	overseerName = os.Getenv("OVERSEER_NAME")
	namespace = os.Getenv("NAMESPACE")

	rootCmd := &cobra.Command{
		Use:   "overseer-cli",
		Short: "CLI for Overseer to manage sandboxes and tasks",
	}

	rootCmd.PersistentFlags().AddGoFlagSet(flag.CommandLine)
	rootCmd.PersistentFlags().BoolVar(&dryRun, "dry-run", false, "Dry run: skip mutations and only log intent")

	rootCmd.AddCommand(buildIssueCommand())
	rootCmd.AddCommand(buildPRCommand())
	rootCmd.AddCommand(buildChoreCommand())
	rootCmd.AddCommand(buildReconcileCommand())
	rootCmd.AddCommand(buildDeleteCommand())

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func buildIssueCommand() *cobra.Command {
	var number int
	var prNumber int
	var taskType string
	var prompt string

	cmd := &cobra.Command{
		Use:   "issue",
		Short: "Create/ensure sandbox and task for an issue",
		Long: `Create/ensure sandbox and task for an issue.

This command is affected by the ISSUE_MODE environment variable:
- enabled (default): Create/ensure sandbox and task
- disabled: Skip execution
- dryrun: Simulate execution without making any changes`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runIssue(cmd.Context(), number, prNumber, taskType, prompt)
		},
	}

	cmd.Flags().IntVar(&number, "number", 0, "Issue number")
	cmd.Flags().IntVar(&prNumber, "pr", 0, "PR number to extract issue from")
	cmd.Flags().StringVar(&taskType, "task", "fix-issue", "Task type (e.g., fix-issue, triage-issue)")
	cmd.Flags().StringVar(&prompt, "prompt", "", "Custom prompt for the task")

	return cmd
}

func buildPRCommand() *cobra.Command {
	var number int
	var taskType string
	var submit bool
	var prompt string

	cmd := &cobra.Command{
		Use:   "pr",
		Short: "Create/ensure sandbox and task for a PR",
		Long: `Create/ensure sandbox and task for a PR.

This command is affected by the PR_MODE and REVIEW_MODE environment variables:
- enabled (default): Create/ensure sandbox and task
- disabled: Skip execution
- dryrun: Simulate execution without making any changes

PR_MODE is used by default. REVIEW_MODE is used if --submit is provided or if task type is "review".`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runPR(cmd.Context(), number, taskType, submit, prompt)
		},
	}

	cmd.Flags().IntVar(&number, "number", 0, "PR number")
	cmd.Flags().StringVar(&taskType, "task", "review", "Task type (e.g., review, address-feedback, investigate-failures)")
	cmd.Flags().BoolVar(&submit, "submit", false, "Submit agent draft from task as review")
	cmd.Flags().StringVar(&prompt, "prompt", "", "Custom prompt for the task")
	_ = cmd.MarkFlagRequired("number")

	return cmd
}

func buildChoreCommand() *cobra.Command {
	var name string
	var file string

	cmd := &cobra.Command{
		Use:   "chore",
		Short: "Create/ensure sandbox and task for a chore",
		Long: `Create/ensure sandbox and task for a chore.

This command is affected by the CHORES_MODE environment variable:
- enabled (default): Create/ensure sandbox and task
- disabled: Skip execution
- dryrun: Simulate execution without making any changes`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runChore(cmd.Context(), name, file)
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Chore name")
	cmd.Flags().StringVar(&file, "file", "", "Chore definition file path")
	_ = cmd.MarkFlagRequired("file")

	return cmd
}

func buildReconcileCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reconcile",
		Args:  cobra.NoArgs,
		Short: "Reconcile sandboxes: delete sandboxes for chores, issues or reviews that are disabled or no longer present",
		Long: `Reconcile sandboxes: delete sandboxes for chores, issues or reviews that are disabled or no longer present.

This command is affected by the following environment variables:
- CHORES_MODE: enabled (default), disabled, or dryrun
- ISSUE_MODE: enabled (default), disabled, or dryrun
- PR_MODE: enabled (default), disabled, or dryrun
- REVIEW_MODE: enabled (default), disabled, or dryrun

The modes are case-insensitive and support boolean equivalents (true/false, on/off, 1/0, etc.) 
and various dry-run spellings (dry-run, dry_run, dry run).`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runReconcile(cmd.Context())
		},
	}
	return cmd
}

func buildDeleteCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete sandboxes and related resources",
	}

	sandboxCmd := &cobra.Command{
		Use:   "sandbox [name...]",
		Short: "Delete one or more sandboxes and their associated resources (like the -lb service)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDeleteSandboxes(cmd.Context(), args)
		},
	}
	cmd.AddCommand(sandboxCmd)

	return cmd
}

type ChoreDefinition struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Schedule    string `json:"schedule"`
	SkipPR      bool   `json:"skipPR,omitempty"`
	Prompt      string `json:"-"`
}

func runChore(ctx context.Context, name string, file string) error {
	if overseerName == "" || namespace == "" {
		return fmt.Errorf("OVERSEER_NAME and NAMESPACE environment variables must be set")
	}

	choresMode := getMode("CHORES_MODE")
	isDryRun := dryRun || choresMode == "dryrun"
	if choresMode == "disabled" {
		klog.Infof("Chore handling is disabled (CHORES_MODE=disabled). Skipping.")
		return nil
	}

	// Validate GitHub token for non-dry-run chores that might need it
	if !isDryRun && os.Getenv("GITHUB_TOKEN") == "" {
		// Try to get token from gh CLI as a validation step
		if _, err := github.GetGithubToken(ctx); err != nil {
			return fmt.Errorf("GitHub token is required for chore execution: %w", err)
		}
	}

	chore, err := parseChore(file)
	if err != nil {
		return err
	}
	if name != "" {
		chore.Name = name
	}
	if chore.Name == "" {
		return fmt.Errorf("chore name is required (either in frontmatter or via --name)")
	}

	cfg, err := config.GetConfig()
	if err != nil {
		return fmt.Errorf("unable to get kubeconfig: %w", err)
	}

	dynClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("unable to create dynamic client: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("unable to create clientset: %w", err)
	}

	kubeClient := &clients.KubernetesClient{
		DynamicClient: dynClient,
		Clientset:     clientset,
	}
	manager := k8s.NewManager(kubeClient)

	rwUnstructured, err := getOverseer(ctx, kubeClient.DynamicClient, overseerName)
	if err != nil {
		return fmt.Errorf("failed to get Overseer %s: %w", overseerName, err)
	}

	var overseer overseerv1alpha1.Overseer
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(rwUnstructured.Object, &overseer); err != nil {
		return fmt.Errorf("failed to convert Overseer: %w", err)
	}

	sandboxName := k8s.TruncateName(fmt.Sprintf("chore-%s-%s", overseer.Name, k8s.Slugify(chore.Name)))

	isPaused := strings.EqualFold(chore.Schedule, "never")
	if !isChoreAllowed(overseer.Spec.Chores, chore.Name) || isPaused {
		reason := "excluded or not included"
		if isPaused {
			reason = "paused (schedule: never)"
		}
		if isDryRun {
			klog.Infof("[dryrun] Chore %s is %s. Would delete sandbox %s and its service if it exists", chore.Name, reason, sandboxName)
			return nil
		}
		klog.Infof("Chore %s is %s. Ensuring sandbox is deleted.", chore.Name, reason)
		return deleteSandbox(ctx, kubeClient, namespace, sandboxName)
	}
	if isDryRun {
		klog.Infof("[dryrun] Would create sandbox and task for chore %s in Overseer %s", chore.Name, overseerName)
		return nil
	}

	ghClient, err := github.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("failed to create github client: %w", err)
	}

	if err := ensureGitHubUser(ctx, ghClient, isDryRun); err != nil {
		return err
	}

	owner, repo, err := parseRepoURL(overseer.Spec.RepoURL)
	if err != nil {
		return fmt.Errorf("failed to parse RepoURL: %w", err)
	}

	// Check if sandbox exists
	_, err = kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Get(ctx, sandboxName, metav1.GetOptions{})
	if err != nil {
		if kerrors.IsNotFound(err) {
			// Create Sandbox
			klog.Infof("Creating sandbox %s...", sandboxName)
			if err := createChoreSandbox(ctx, kubeClient, &overseer, chore, sandboxName); err != nil {
				return fmt.Errorf("failed to create chore sandbox: %w", err)
			}
		} else {
			return fmt.Errorf("failed to check if sandbox exists: %w", err)
		}
	}

	// Create Task
	taskType := "chore"
	klog.Infof("Creating task %s for sandbox %s...", taskType, sandboxName)
	params := map[string]string{
		"AGENT_PROMPT": chore.Prompt,
		"CHORE_NAME":   chore.Name,
		"CHORE_FILE":   file,
		"REPO_OWNER":   owner,
		"REPO_NAME":    repo,
		"CLONE_URL":    overseer.Spec.RepoURL,
	}
	if chore.SkipPR {
		params["SKIP_PR"] = "true"
	}

	err = manager.CreateSandboxTask(ctx, namespace, sandboxName, "Sandbox", taskType, params)
	if err != nil {
		return fmt.Errorf("failed to create sandbox task: %w", err)
	}

	klog.Infof("Done.")
	return nil
}

var (
	ErrMissingFrontmatter = errors.New("missing frontmatter")
)

func parseChore(path string) (*ChoreDefinition, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	parts := strings.SplitN(string(data), "---", 3)
	if len(parts) < 3 {
		return nil, ErrMissingFrontmatter
	}

	var chore ChoreDefinition
	if err := yaml.Unmarshal([]byte(parts[1]), &chore); err != nil {
		return nil, fmt.Errorf("failed to unmarshal frontmatter in %s: %w", path, err)
	}

	chore.Prompt = strings.TrimSpace(parts[2])
	return &chore, nil
}

func createChoreSandbox(ctx context.Context, kubeClient *clients.KubernetesClient, overseer *overseerv1alpha1.Overseer, chore *ChoreDefinition, sandboxName string) error {
	cloneURL := strings.TrimRight(overseer.Spec.RepoURL, "/")
	if !strings.HasSuffix(cloneURL, ".git") {
		cloneURL += ".git"
	}

	_, repo, err := parseRepoURL(overseer.Spec.RepoURL)
	if err != nil {
		return fmt.Errorf("failed to parse RepoURL: %w", err)
	}

	apiKeySecretName := overseer.Spec.GeminiAPIKeySecretName
	if apiKeySecretName == "" {
		apiKeySecretName = "gemini-api-key"
	}

	githubSecretName := overseer.Spec.RobotAccount

	scriptToken, err := getTokenFromScript()
	if err != nil {
		klog.Warningf("failed to get token from script: %v", err)
	}

	githubAPIURL := os.Getenv("GITHUB_API_URL")
	var ghHost string
	if githubAPIURL != "" {
		u, err := url.Parse(githubAPIURL)
		if err == nil && u.Host != "" {
			ghHost = u.Host
		}
	}

	if scriptToken != "" {
		apiKeySecretName = sandboxName + "-api-key"
		klog.Infof("Creating API key secret %s...", apiKeySecretName)
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      apiKeySecretName,
				Namespace: namespace,
				Labels: map[string]string{
					"sandbox.gemini.google.com/sandbox-name": k8s.TruncateLabel(sandboxName),
				},
			},
			Data: map[string][]byte{
				"gemini": []byte(scriptToken),
			},
		}
		_, err = kubeClient.Clientset.CoreV1().Secrets(namespace).Create(ctx, secret, metav1.CreateOptions{})
		if err != nil && !kerrors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create API key secret: %w", err)
		}
	}

	opt := sandbox.AgentSandboxOptions{
		DevSandboxOptions: sandbox.DevSandboxOptions{
			Name:      sandboxName,
			Namespace: namespace,
			Labels: map[string]string{
				"review.gemini.google.com/overseer":      k8s.TruncateLabel(overseer.Name),
				"sandbox.gemini.google.com/type":         "chore",
				"chore.gemini.google.com/name":           k8s.TruncateLabel(k8s.Slugify(chore.Name)),
				"sandbox.gemini.google.com/name":         k8s.TruncateLabel(sandboxName),
				"sandbox.gemini.google.com/sandbox-name": k8s.TruncateLabel(sandboxName),
				"sandbox":                                k8s.TruncateLabel(sandboxName), // Legacy support
			},
			CloneURL:            cloneURL,
			HTMLURL:             strings.TrimSuffix(overseer.Spec.RepoURL, ".git"),
			Branch:              "main", // Default branch for chores
			Origin:              fmt.Sprintf("github.com/%s/%s", githubUserLogin, repo),
			PushEnabled:         true,
			UserLogin:           githubUserLogin,
			UserName:            githubUserName,
			UserEmail:           githubUserEmail,
			BotLogin:            githubBotLogin,
			BotName:             githubBotName,
			BotEmail:            githubBotEmail,
			LLMAPIKeySecretName: apiKeySecretName,
			GithubSecretName:    githubSecretName,
			LLMAPIKey:           "", // scriptToken is now passed via secret
			OverseerName:        overseerName,
			RepoSandboxImage:    os.Getenv("REPO_SANDBOX_IMAGE"),
			ConfigDirImage:      os.Getenv("CONFIG_DIR_IMAGE"),
			HTTPEnabled:         true,
			Replicas:            1,
			WorkspaceDiskSize:   overseer.Spec.WorkspaceDiskSize,
			ServiceAccountName:  "overseer-sandbox",
			GHHost:              ghHost,
		},
		IssueRepo: repo,
	}

	opt.LLMProvider = "gemini-cli"
	opt.LLMConfigdirRef = overseer.Spec.ConfigdirRef
	opt.Image = overseer.Spec.Image

	sb, svc := sandbox.NewAgentSandbox(opt)
	sb.SetName(sandboxName)

	if svc.Labels == nil {
		svc.Labels = make(map[string]string)
	}
	svc.Labels["sandbox.gemini.google.com/name"] = k8s.TruncateLabel(sandboxName)
	svc.Labels["sandbox"] = k8s.TruncateLabel(sandboxName) // Legacy support

	_, err = kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Create(ctx, sb, metav1.CreateOptions{})
	if err != nil {
		return err
	}

	_, err = kubeClient.Clientset.CoreV1().Services(namespace).Create(ctx, svc, metav1.CreateOptions{})
	return err
}

func runIssue(ctx context.Context, number int, prNumber int, taskType string, customPrompt string) error {
	if overseerName == "" || namespace == "" {
		return fmt.Errorf("OVERSEER_NAME and NAMESPACE environment variables must be set")
	}

	if number <= 0 && prNumber <= 0 {
		return fmt.Errorf("either issue number or PR number (to resolve issue from) must be greater than zero")
	}
	if prNumber < 0 {
		return fmt.Errorf("PR number must be greater than or equal to zero")
	}

	issueMode := getMode("ISSUE_MODE")
	isDryRun := dryRun || issueMode == "dryrun"
	if issueMode == "disabled" {
		klog.Infof("Issue handling is disabled (ISSUE_MODE=disabled). Skipping.")
		return nil
	}

	cfg, err := config.GetConfig()
	if err != nil {
		return fmt.Errorf("unable to get kubeconfig: %w", err)
	}

	dynClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("unable to create dynamic client: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("unable to create clientset: %w", err)
	}

	kubeClient := &clients.KubernetesClient{
		DynamicClient: dynClient,
		Clientset:     clientset,
	}
	manager := k8s.NewManager(kubeClient)

	rwUnstructured, err := getOverseer(ctx, kubeClient.DynamicClient, overseerName)
	if err != nil {
		return fmt.Errorf("failed to get Overseer %s: %w", overseerName, err)
	}

	var overseer overseerv1alpha1.Overseer
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(rwUnstructured.Object, &overseer); err != nil {
		return fmt.Errorf("failed to convert Overseer: %w", err)
	}

	owner, repo, err := parseRepoURL(overseer.Spec.RepoURL)
	if err != nil {
		return fmt.Errorf("failed to parse RepoURL: %w", err)
	}

	if number == 0 && prNumber != 0 {
		klog.Infof("Resolving issue from PR %d...", prNumber)
		if isDryRun {
			klog.Infof("[dryrun] Would resolve issue from PR %d", prNumber)
			number = prNumber
		} else {
			number, err = resolveIssueFromPR(ctx, owner, repo, prNumber)
			if err != nil {
				return fmt.Errorf("failed to resolve issue from PR: %w", err)
			}
			klog.Infof("Resolved to issue %d", number)
		}
	}

	if number <= 0 {
		return fmt.Errorf("issue number must be greater than zero")
	}

	if isDryRun {
		klog.Infof("[dryrun] Would create/ensure sandbox and task %s for issue %d in Overseer %s", taskType, number, overseerName)
		return nil
	}

	ghClient, err := github.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("failed to create github client: %w", err)
	}
	if apiURL := os.Getenv("GITHUB_API_URL"); apiURL != "" {
		u, err := url.Parse(apiURL)
		if err != nil {
			return fmt.Errorf("invalid GITHUB_API_URL: %w", err)
		}
		ghClient.BaseURL = u
	}

	issue, _, err := ghClient.Issues.Get(ctx, owner, repo, number)
	if err != nil {
		return fmt.Errorf("failed to get issue %d: %w", number, err)
	}
	issueTitle := issue.GetTitle()

	if err := ensureGitHubUser(ctx, ghClient, isDryRun); err != nil {
		return err
	}

	sandboxName := k8s.TruncateName(fmt.Sprintf("%s-issue-%d", overseer.Name, number))

	var sandboxExists bool
	var sandboxIsActive bool
	sandboxUnstructured, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Get(ctx, sandboxName, metav1.GetOptions{})
	if err == nil {
		sandboxExists = true
		replicas, found, err := unstructured.NestedInt64(sandboxUnstructured.Object, "spec", "replicas")
		if err == nil && (!found || replicas > 0) {
			sandboxIsActive = true
		}
	} else if !kerrors.IsNotFound(err) {
		return fmt.Errorf("failed to check if sandbox exists: %w", err)
	}

	// Check limit only if we need to create or activate a sandbox
	if !sandboxIsActive && overseer.Spec.MaxActiveIssues != nil {
		maxIssues := *overseer.Spec.MaxActiveIssues
		activeCount, err := countActiveSandboxes(ctx, kubeClient.DynamicClient, namespace, overseerName, "issue")
		if err != nil {
			return fmt.Errorf("failed to count active issue sandboxes: %w", err)
		}
		if int32(activeCount) >= maxIssues {
			// Instead of returning nil, return an error so overseer logs it and skips
			return fmt.Errorf("limit_reached: max active issues limit (%d) reached (currently %d active)", maxIssues, activeCount)
		}
	}

	// Create Sandbox if it doesn't exist
	if !sandboxExists {
		klog.Infof("Creating sandbox %s for issue %q...", sandboxName, issueTitle)
		if err := createIssueSandbox(ctx, kubeClient, &overseer, issue); err != nil {
			return fmt.Errorf("failed to create issue sandbox: %w", err)
		}
	}

	// Create Task
	klog.Infof("Creating task %s for sandbox %s...", taskType, sandboxName)
	agentPrompt := customPrompt
	if agentPrompt == "" {
		agentPrompt = overseer.Spec.IssuePrompt
	}

	params := map[string]string{
		"ISSUE_URL":       issue.GetHTMLURL(),
		"PULL_REQUEST_ID": fmt.Sprintf("%d", prNumber),
		"AGENT_PROMPT":    agentPrompt,
		"PR_LABEL":        "overseer",
	}
	params["model"] = strings.Join(IssueModelsOrder, ",")

	err = manager.CreateSandboxTask(ctx, namespace, sandboxName, "Sandbox", taskType, params)
	if err != nil {
		return fmt.Errorf("failed to create sandbox task: %w", err)
	}

	klog.Infof("Done.")
	return nil
}

func runPR(ctx context.Context, number int, taskType string, submit bool, customPrompt string) error {
	// Similar to runIssue but for PRs
	if overseerName == "" || namespace == "" {
		return fmt.Errorf("OVERSEER_NAME and NAMESPACE environment variables must be set")
	}

	if number <= 0 {
		return fmt.Errorf("PR number must be greater than zero")
	}

	modeName := "PR_MODE"
	if submit || taskType == "review" {
		modeName = "REVIEW_MODE"
	}
	mode := getMode(modeName)
	isDryRun := dryRun || mode == "dryrun"

	if mode == "disabled" {
		klog.Infof("PR/Review handling is disabled (%s=disabled). Skipping.", modeName)
		return nil
	}

	cfg, err := config.GetConfig()
	if err != nil {
		return fmt.Errorf("unable to get kubeconfig: %w", err)
	}

	dynClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("unable to create dynamic client: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("unable to create clientset: %w", err)
	}

	kubeClient := &clients.KubernetesClient{
		DynamicClient: dynClient,
		Clientset:     clientset,
	}
	manager := k8s.NewManager(kubeClient)

	if submit {
		return submitAgentDraft(ctx, manager, kubeClient, namespace, overseerName, number, isDryRun)
	}

	rwUnstructured, err := getOverseer(ctx, kubeClient.DynamicClient, overseerName)
	if err != nil {
		return fmt.Errorf("failed to get Overseer %s: %w", overseerName, err)
	}

	var overseer overseerv1alpha1.Overseer
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(rwUnstructured.Object, &overseer); err != nil {
		return fmt.Errorf("failed to convert Overseer: %w", err)
	}

	owner, repo, err := parseRepoURL(overseer.Spec.RepoURL)
	if err != nil {
		return fmt.Errorf("failed to parse RepoURL: %w", err)
	}

	if isDryRun {
		klog.Infof("[dryrun] Would create/ensure sandbox and task %s for PR %d in Overseer %s", taskType, number, overseerName)
		return nil
	}

	ghClient, err := github.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("failed to create github client: %w", err)
	}
	if apiURL := os.Getenv("GITHUB_API_URL"); apiURL != "" {
		u, err := url.Parse(apiURL)
		if err != nil {
			return fmt.Errorf("invalid GITHUB_API_URL: %w", err)
		}
		ghClient.BaseURL = u
	}

	pr, _, err := ghClient.PullRequests.Get(ctx, owner, repo, number)
	if err != nil {
		return fmt.Errorf("failed to get PR %d: %w", number, err)
	}

	if err := ensureGitHubUser(ctx, ghClient, isDryRun); err != nil {
		return err
	}

	sandboxName := k8s.TruncateName(fmt.Sprintf("%s-pr-%d", overseer.Name, number))
	headSHA := pr.GetHead().GetSHA()

	var sandboxExists bool
	var sandboxIsActive bool
	sandboxUnstructured, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Get(ctx, sandboxName, metav1.GetOptions{})
	if err == nil {
		sandboxExists = true
		replicas, found, err := unstructured.NestedInt64(sandboxUnstructured.Object, "spec", "replicas")
		if err == nil && (!found || replicas > 0) {
			sandboxIsActive = true
		}
	} else if !kerrors.IsNotFound(err) {
		return fmt.Errorf("failed to check if sandbox exists: %w", err)
	}

	// Check limit only if we need to create or activate a sandbox
	if !sandboxIsActive && overseer.Spec.MaxActiveReviews != nil {
		maxReviews := *overseer.Spec.MaxActiveReviews
		activeCount, err := countActiveSandboxes(ctx, kubeClient.DynamicClient, namespace, overseerName, "review")
		if err != nil {
			return fmt.Errorf("failed to count active review sandboxes: %w", err)
		}
		if int32(activeCount) >= maxReviews {
			return fmt.Errorf("limit_reached: max active reviews limit (%d) reached (currently %d active)", maxReviews, activeCount)
		}
	}

	// Check if a task for this SHA already exists (only for review tasks)
	if taskType == "review" {
		taskList, err := manager.ListSandboxTasks(ctx, namespace, k8s.TruncateLabel(sandboxName))
		if err == nil {
			for i := range taskList.Items {
				task := &taskList.Items[i]
				if task.Spec.Type == "review" && task.Spec.Params["HEAD_SHA"] == headSHA {
					if task.Status.TaskState == "Completed" || task.Status.TaskState == "Running" || task.Status.TaskState == "Pending" {
						klog.Infof("Review task for SHA %s already exists in state %s. Skipping.", headSHA, task.Status.TaskState)
						return nil
					}
				}
			}
		}
	}

	// Create Sandbox if it doesn't exist
	if !sandboxExists {
		klog.Infof("Creating sandbox %s for PR %d %q...", sandboxName, number, pr.GetTitle())
		if err := createPRSandbox(ctx, kubeClient, &overseer, pr); err != nil {
			return fmt.Errorf("failed to create PR sandbox: %w", err)
		}
	}

	// Create Task
	klog.Infof("Creating task %s for sandbox %s...", taskType, sandboxName)
	agentPrompt := customPrompt
	if agentPrompt == "" {
		agentPrompt = overseer.Spec.Review.Prompt
	}

	params := map[string]string{
		"PULL_REQUEST_ID": fmt.Sprintf("%d", number),
		"ISSUE_URL":       pr.GetHTMLURL(),
		"AGENT_PROMPT":    agentPrompt,
		"HEAD_SHA":        headSHA,
	}

	err = manager.CreateSandboxTask(ctx, namespace, sandboxName, "Sandbox", taskType, params)
	if err != nil {
		return fmt.Errorf("failed to create sandbox task: %w", err)
	}

	klog.Infof("Done.")
	return nil
}

func submitAgentDraft(ctx context.Context, manager *k8s.Manager, kubeClient *clients.KubernetesClient, namespace, overseerName string, prNumber int, isDryRun bool) error {
	rwUnstructured, err := getOverseer(ctx, kubeClient.DynamicClient, overseerName)
	if err != nil {
		return fmt.Errorf("failed to get Overseer %s: %w", overseerName, err)
	}

	sandboxName := k8s.TruncateName(fmt.Sprintf("%s-pr-%d", overseerName, prNumber))

	taskList, err := manager.ListSandboxTasks(ctx, namespace, k8s.TruncateLabel(sandboxName))
	if err != nil {
		return fmt.Errorf("failed to list tasks for sandbox %s: %w", sandboxName, err)
	}

	var latestReviewTask *sandboxtaskv1alpha1.SandboxTask
	for i := range taskList.Items {
		task := &taskList.Items[i]
		if task.Spec.Type == "review" && task.Status.TaskState == "Completed" {
			if latestReviewTask == nil || task.CreationTimestamp.After(latestReviewTask.CreationTimestamp.Time) {
				latestReviewTask = task
			}
		}
	}

	if latestReviewTask == nil {
		return fmt.Errorf("no completed review task found for sandbox %s", sandboxName)
	}

	currentSHA := latestReviewTask.Spec.Params["HEAD_SHA"]

	sandboxUnstructured, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Get(ctx, sandboxName, metav1.GetOptions{})
	if err == nil {
		annotations := sandboxUnstructured.GetAnnotations()
		if annotations != nil {
			if state, ok := annotations["reviewState"]; ok {
				if state == "submitted" {
					klog.Infof("Review for PR %d already submitted (legacy).", prNumber)
					return nil
				}
				if currentSHA != "" && state == "submitted:"+currentSHA {
					klog.Infof("Review for PR %d and SHA %s already submitted.", prNumber, currentSHA)
					return nil
				}
			}
		}
	}

	// Get the task again as Unstructured to read annotations
	gvr := schema.GroupVersionResource{
		Group:    "custom.agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "sandboxtasks",
	}
	taskUnstructured, err := kubeClient.DynamicClient.Resource(gvr).Namespace(namespace).Get(ctx, latestReviewTask.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get task %s: %w", latestReviewTask.Name, err)
	}

	annotations := taskUnstructured.GetAnnotations()
	draft, ok := annotations["agentDraft"]
	if !ok {
		return fmt.Errorf("no agentDraft annotation found on task %s", latestReviewTask.Name)
	}

	var overseer overseerv1alpha1.Overseer
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(rwUnstructured.Object, &overseer); err != nil {
		return fmt.Errorf("failed to convert Overseer: %w", err)
	}

	var owner, repoName string
	var client *githubv39.Client
	if !isDryRun {
		token := os.Getenv("GITHUB_TOKEN")
		if token == "" {
			githubSecretName := overseer.Spec.RobotAccount

			rwUnstructuredCopy := rwUnstructured.DeepCopy()
			_ = unstructured.SetNestedField(rwUnstructuredCopy.Object, githubSecretName, "spec", "githubSecretName")
			// workaround since GetGitHubToken expects the secret name to be in the spec, but our unstructured doesn't have it set there
			// all requires namespace
			_ = unstructured.SetNestedField(rwUnstructuredCopy.Object, namespace, "metadata", "namespace")

			// Get GitHub token from secret
			token, err = manager.GetGitHubToken(ctx, rwUnstructuredCopy)
			if err != nil {
				return fmt.Errorf("failed to get github token: %w", err)
			}
		}

		// Create GitHub client
		client = clients.NewGitHubClient(ctx, token)
		if apiURL := os.Getenv("GITHUB_API_URL"); apiURL != "" {
			u, err := url.Parse(apiURL)
			if err != nil {
				return fmt.Errorf("invalid GITHUB_API_URL: %w", err)
			}
			client.BaseURL = u
		}

		// Parse repo URL
		repoURL, found, err := unstructured.NestedString(rwUnstructured.Object, "spec", "repoURL")
		if err != nil || !found {
			return fmt.Errorf("repoURL not found in Overseer %s", overseerName)
		}
		owner, repoName, err = parseRepoURL(repoURL)
		if err != nil {
			return fmt.Errorf("failed to parse repo URL %s: %w", repoURL, err)
		}
	}

	// Try Unmarshalling the yaml review payload into PullRequestReviewRequest
	agentOutput := &models.ReviewAgentOutput{}
	reviewRequest := &githubv39.PullRequestReviewRequest{}
	err = yaml.Unmarshal([]byte(draft), &agentOutput)
	if err != nil || agentOutput.Review == nil {
		if err != nil {
			klog.Warningf("failed to unmarshal review payload as YAML, using as plain body: %v", err)
		} else {
			klog.Warningf("review field missing in YAML, using draft as plain body")
		}

		reviewRequest.Body = githubv39.String(draft)
	} else {
		reviewRequest = agentOutput.Review.ToGitHubReviewRequest()
	}

	if isDryRun {
		// Try to parse repo URL for better logging if possible, but don't fail if it's missing
		repoURL, _, _ := unstructured.NestedString(rwUnstructured.Object, "spec", "repoURL")
		owner, repoName, _ = parseRepoURL(repoURL)
		if owner == "" {
			owner = "unknown-owner"
		}
		if repoName == "" {
			repoName = "unknown-repo"
		}
		klog.Infof("[dryrun] Would create review on GitHub for %s/%s PR %d (found task %s)", owner, repoName, prNumber, latestReviewTask.Name)
		return nil
	}

	// Set event to COMMENT to submit directly instead of creating a draft
	reviewRequest.Event = githubv39.String("COMMENT")

	klog.Infof("Creating review on GitHub for %s/%s PR %d...", owner, repoName, prNumber)
	review, _, err := client.PullRequests.CreateReview(ctx, owner, repoName, prNumber, reviewRequest)
	if err != nil {
		return fmt.Errorf("failed to create review on GitHub: %w", err)
	}
	klog.Infof("Successfully created review: %s", review.GetHTMLURL())

	// Update sandbox reviewState
	reviewState := "submitted"
	if currentSHA != "" {
		reviewState = "submitted:" + currentSHA
	}
	if err := manager.UpdateSandboxAnnotation(ctx, namespace, sandboxName, "reviewState", reviewState); err != nil {
		klog.Warningf("failed to update reviewState annotation: %v", err)
	}

	klog.Infof("Done.")
	return nil
}

var (
	githubUserLogin string
	githubUserName  string
	githubUserEmail string
	githubBotLogin  string
	githubBotName   string
	githubBotEmail  string
)

func ensureGitHubUser(ctx context.Context, ghClient *github.Client, isDryRun bool) error {
	if githubBotLogin == "" {
		githubBotLogin = os.Getenv("GITHUB_BOT_LOGIN")
		githubBotName = os.Getenv("GITHUB_BOT_NAME")
		githubBotEmail = os.Getenv("GITHUB_BOT_EMAIL")
	}

	if githubUserLogin != "" {
		if githubUserEmail == "" {
			githubUserEmail = githubUserLogin + "@users.noreply.github.com"
		}
		return nil
	}
	githubUserLogin = os.Getenv("GITHUB_USER_ID")
	githubUserName = os.Getenv("GITHUB_USER_NAME")
	githubUserEmail = os.Getenv("GITHUB_USER_EMAIL")

	if githubUserLogin != "" {
		if githubUserName == "" {
			githubUserName = githubUserLogin
		}
		if githubUserEmail == "" {
			githubUserEmail = githubUserLogin + "@users.noreply.github.com"
		}
		return nil
	}

	if isDryRun {
		klog.Infof("[dryrun] Skipping GitHub user info fetch. Using defaults.")
		githubUserLogin = "dryrun-user"
		githubUserName = "Dryrun User"
		githubUserEmail = "dryrun@example.com"
		return nil
	}

	user, _, err := ghClient.Users.Get(ctx, "")
	if err != nil {
		return fmt.Errorf("failed to fetch GitHub user info: %w", err)
	}

	githubUserLogin = user.GetLogin()
	githubUserName = user.GetName()
	if githubUserName == "" {
		githubUserName = githubUserLogin
	}
	githubUserEmail = user.GetEmail()
	if githubUserEmail == "" {
		githubUserEmail = githubUserLogin + "@users.noreply.github.com"
	}
	return nil
}

func parseRepoURL(repoURL string) (string, string, error) {
	if repoURL == "" {
		return "", "", fmt.Errorf("empty repository URL")
	}

	// Handle SSH URLs like git@github.com:owner/repo or github.com:owner/repo
	// Standard SSH detection: contains : but no ://, OR strictly matching git@ prefix
	isSSH := (strings.Contains(repoURL, ":") && !strings.Contains(repoURL, "://")) || strings.HasPrefix(repoURL, "git@")
	if isSSH {
		// Clean up common suffixes/separators before splitting
		s := strings.SplitN(repoURL, "?", 2)[0]
		s = strings.SplitN(s, "#", 2)[0]
		s = strings.TrimRight(s, "/")
		s = strings.TrimSuffix(s, ".git")

		// For SSH URLs, owner/repo follows the colon
		parts := strings.SplitN(s, ":", 2)
		if len(parts) == 2 {
			repoPath := parts[1]
			pathParts := strings.Split(strings.Trim(repoPath, "/"), "/")
			if len(pathParts) >= 2 {
				owner := strings.Join(pathParts[:len(pathParts)-1], "/")
				repo := pathParts[len(pathParts)-1]
				return owner, repo, nil
			}
		}
	}

	// Handle local paths
	isLocal := strings.HasPrefix(repoURL, "/") || strings.HasPrefix(repoURL, "./") || strings.HasPrefix(repoURL, "../")
	// Windows support: check if it's an absolute path (e.g., C:\...)
	if !isLocal && filepath.IsAbs(repoURL) {
		isLocal = true
	}

	uStr := repoURL
	if !strings.Contains(uStr, "://") && !isLocal {
		uStr = "https://" + uStr
	}

	// For non-local, non-SSH URLs, use standard URL parser
	if !isLocal {
		u, err := url.Parse(uStr)
		if err != nil {
			return "", "", err
		}

		// Support git://, https://, http:// etc.
		path := strings.Trim(u.Path, "/")
		path = strings.TrimSuffix(path, ".git")
		parts := strings.Split(path, "/")
		if len(parts) >= 2 {
			owner := strings.Join(parts[:len(parts)-1], "/")
			return owner, parts[len(parts)-1], nil
		}
	}

	// Fallback for local paths or if URL parsing didn't give enough parts
	path := strings.Trim(repoURL, "/")
	path = strings.TrimSuffix(path, ".git")
	parts := strings.Split(path, "/")
	if len(parts) >= 2 {
		owner := strings.Join(parts[:len(parts)-1], "/")
		return owner, parts[len(parts)-1], nil
	}

	return "", "", fmt.Errorf("invalid repository URL format: %s (need at least owner and repo name)", repoURL)
}

func createIssueSandbox(ctx context.Context, kubeClient *clients.KubernetesClient, overseer *overseerv1alpha1.Overseer, issue *githubv39.Issue) error {
	// Replicate logic from repowatch_controller.go:createIssueSandbox
	name := k8s.TruncateName(fmt.Sprintf("%s-issue-%d", overseer.Name, issue.GetNumber()))
	cloneURL := strings.Replace(issue.GetRepositoryURL(), "api.github.com/repos", "github.com", 1) + ".git"

	_, repo, err := parseRepoURL(overseer.Spec.RepoURL)
	if err != nil {
		return fmt.Errorf("failed to parse RepoURL: %w", err)
	}

	branchName := fmt.Sprintf("issue-%d-%s", issue.GetNumber(), randString(4))

	apiKeySecretName := overseer.Spec.GeminiAPIKeySecretName
	if apiKeySecretName == "" {
		apiKeySecretName = "gemini-api-key"
	}

	githubSecretName := overseer.Spec.RobotAccount

	scriptToken, err := getTokenFromScript()
	if err != nil {
		klog.Warningf("failed to get token from script: %v", err)
	}

	githubAPIURL := os.Getenv("GITHUB_API_URL")
	var ghHost string
	if githubAPIURL != "" {
		u, err := url.Parse(githubAPIURL)
		if err == nil && u.Host != "" {
			ghHost = u.Host
		}
	}

	if scriptToken != "" {
		apiKeySecretName = name + "-api-key"
		klog.Infof("Creating API key secret %s...", apiKeySecretName)
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      apiKeySecretName,
				Namespace: namespace,
				Labels: map[string]string{
					"sandbox.gemini.google.com/sandbox-name": k8s.TruncateLabel(name),
				},
			},
			Data: map[string][]byte{
				"gemini": []byte(scriptToken),
			},
		}
		_, err = kubeClient.Clientset.CoreV1().Secrets(namespace).Create(ctx, secret, metav1.CreateOptions{})
		if err != nil && !kerrors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create API key secret: %w", err)
		}
	}

	opt := sandbox.AgentSandboxOptions{
		DevSandboxOptions: sandbox.DevSandboxOptions{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"review.gemini.google.com/overseer":      k8s.TruncateLabel(overseer.Name),
				"sandbox.gemini.google.com/type":         "issue",
				"issue.gemini.google.com/number":         fmt.Sprintf("%d", issue.GetNumber()),
				"sandbox.gemini.google.com/name":         k8s.TruncateLabel(name),
				"sandbox.gemini.google.com/sandbox-name": k8s.TruncateLabel(name),
				"sandbox":                                k8s.TruncateLabel(name), // Legacy support
			},
			CloneURL:            cloneURL,
			HTMLURL:             issue.GetHTMLURL(),
			Branch:              branchName,
			Origin:              fmt.Sprintf("github.com/%s/%s", githubUserLogin, repo),
			PushEnabled:         false,
			UserLogin:           githubUserLogin,
			UserName:            githubUserName,
			UserEmail:           githubUserEmail,
			BotLogin:            githubBotLogin,
			BotName:             githubBotName,
			BotEmail:            githubBotEmail,
			LLMProvider:         "gemini-cli",
			LLMConfigdirRef:     overseer.Spec.ConfigdirRef,
			LLMAPIKeySecretName: apiKeySecretName,
			Prompt:              overseer.Spec.IssuePrompt,
			GithubSecretName:    githubSecretName,
			Image:               overseer.Spec.Image,
			RepoSandboxImage:    os.Getenv("REPO_SANDBOX_IMAGE"),
			ConfigDirImage:      os.Getenv("CONFIG_DIR_IMAGE"),
			HTTPEnabled:         true,
			Replicas:            1,
			ServiceAccountName:  "overseer-sandbox",
			WorkspaceDiskSize:   overseer.Spec.WorkspaceDiskSize,
			GHHost:              ghHost,
		},
		IssueID:    fmt.Sprintf("%d", issue.GetNumber()),
		IssueTitle: issue.GetTitle(),
		IssueRepo:  repo,
	}

	sb, svc := sandbox.NewAgentSandbox(opt)

	if svc.Labels == nil {
		svc.Labels = make(map[string]string)
	}
	svc.Labels["sandbox.gemini.google.com/name"] = k8s.TruncateLabel(name)
	svc.Labels["sandbox"] = k8s.TruncateLabel(name) // Legacy support

	_, err = kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Create(ctx, sb, metav1.CreateOptions{})
	if err != nil {
		return err
	}

	_, err = kubeClient.Clientset.CoreV1().Services(namespace).Create(ctx, svc, metav1.CreateOptions{})
	return err
}

func createPRSandbox(ctx context.Context, kubeClient *clients.KubernetesClient, overseer *overseerv1alpha1.Overseer, pr *githubv39.PullRequest) error {
	name := k8s.TruncateName(fmt.Sprintf("%s-pr-%d", overseer.Name, pr.GetNumber()))

	_, repo, err := parseRepoURL(overseer.Spec.RepoURL)
	if err != nil {
		return fmt.Errorf("failed to parse RepoURL: %w", err)
	}

	apiKeySecretName := overseer.Spec.GeminiAPIKeySecretName
	if apiKeySecretName == "" {
		apiKeySecretName = "gemini-api-key"
	}

	maxReviewFiles := overseer.Spec.Review.MaxReviewFiles
	if maxReviewFiles == 0 {
		maxReviewFiles = 150
	}
	githubSecretName := overseer.Spec.RobotAccount

	scriptToken, err := getTokenFromScript()
	if err != nil {
		klog.Warningf("failed to get token from script: %v", err)
	}

	githubAPIURL := os.Getenv("GITHUB_API_URL")
	var ghHost string
	if githubAPIURL != "" {
		u, err := url.Parse(githubAPIURL)
		if err == nil && u.Host != "" {
			ghHost = u.Host
		}
	}

	if scriptToken != "" {
		apiKeySecretName = name + "-api-key"
		klog.Infof("Creating API key secret %s...", apiKeySecretName)
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      apiKeySecretName,
				Namespace: namespace,
				Labels: map[string]string{
					"sandbox.gemini.google.com/sandbox-name": k8s.TruncateLabel(name),
				},
			},
			Data: map[string][]byte{
				"gemini": []byte(scriptToken),
			},
		}
		_, err = kubeClient.Clientset.CoreV1().Secrets(namespace).Create(ctx, secret, metav1.CreateOptions{})
		if err != nil && !kerrors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create API key secret: %w", err)
		}
	}

	opt := sandbox.ReviewSandboxOptions{
		DevSandboxOptions: sandbox.DevSandboxOptions{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"review.gemini.google.com/overseer":      k8s.TruncateLabel(overseer.Name),
				"sandbox.gemini.google.com/type":         "review",
				"pr.gemini.google.com/number":            fmt.Sprintf("%d", pr.GetNumber()),
				"sandbox.gemini.google.com/name":         k8s.TruncateLabel(name),
				"sandbox.gemini.google.com/sandbox-name": k8s.TruncateLabel(name),
				"sandbox":                                k8s.TruncateLabel(name), // Legacy support
			},
			UserLogin:             githubUserLogin,
			UserName:              githubUserName,
			UserEmail:             githubUserEmail,
			BotLogin:              githubBotLogin,
			BotName:               githubBotName,
			BotEmail:              githubBotEmail,
			LLMProvider:           "gemini-cli",
			LLMConfigdirRef:       overseer.Spec.ConfigdirRef,
			LLMAPIKeySecretName:   apiKeySecretName,
			Prompt:                overseer.Spec.Review.Prompt,
			GithubSecretName:      githubSecretName,
			DevcontainerConfigRef: "",
			Image:                 overseer.Spec.Image,
			RepoSandboxImage:      os.Getenv("REPO_SANDBOX_IMAGE"),
			ConfigDirImage:        os.Getenv("CONFIG_DIR_IMAGE"),
			HTTPEnabled:           true,
			Replicas:              1,
			ServiceAccountName:    "overseer-sandbox",
			GHHost:                ghHost,
		},

		PRNumber:          pr.GetNumber(),
		PRTitle:           pr.GetTitle(),
		PRHTMLURL:         pr.GetHTMLURL(),
		PRDiffURL:         pr.GetDiffURL(),
		PRCloneURL:        fmt.Sprintf("%s#refs/heads/%s", pr.GetHead().GetRepo().GetCloneURL(), pr.GetHead().GetRef()),
		RepoName:          repo,
		MaxReviewFiles:    maxReviewFiles,
		IgnoreFiles:       overseer.Spec.Review.IgnoreFiles,
		SeverityThreshold: overseer.Spec.Review.SeverityThreshold,
		LLMExtensions:     overseer.Spec.Extensions,
		WorkspaceDiskSize: overseer.Spec.WorkspaceDiskSize,
	}

	sb, svc := sandbox.NewReviewSandbox(opt)
	// Ensure the service has the sandbox label for robust cleanup via LabelSelector
	if svc.Labels == nil {
		svc.Labels = make(map[string]string)
	}
	svc.Labels["sandbox.gemini.google.com/name"] = k8s.TruncateLabel(name)
	svc.Labels["sandbox"] = k8s.TruncateLabel(name) // Legacy support

	_, err = kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Create(ctx, sb, metav1.CreateOptions{})
	if err != nil {
		return err
	}

	_, err = kubeClient.Clientset.CoreV1().Services(namespace).Create(ctx, svc, metav1.CreateOptions{})
	return err
}

var letterBytes = "abcdefghijklmnopqrstuvwxyz0123456789"
var seededRand = rand.New(rand.NewSource(time.Now().UnixNano()))

func randString(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = letterBytes[seededRand.Intn(len(letterBytes))]
	}
	return string(b)
}

func resolveIssueFromPR(ctx context.Context, owner, repo string, prNumber int) (int, error) {
	// Try using gh CLI to get closing issues
	cmd := exec.CommandContext(ctx, "gh", "pr", "view", fmt.Sprintf("%d", prNumber), "--repo", fmt.Sprintf("%s/%s", owner, repo), "--json", "closingIssuesReferences")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err == nil {
		var result struct {
			ClosingIssuesReferences []struct {
				Number int `json:"number"`
			} `json:"closingIssuesReferences"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &result); err == nil && len(result.ClosingIssuesReferences) > 0 {
			return result.ClosingIssuesReferences[0].Number, nil
		}
	}

	// Fallback: just return the PR number as its own issue number
	return prNumber, nil
}

func countActiveSandboxes(ctx context.Context, dynClient dynamic.Interface, namespace, overseerName, sandboxType string) (int, error) {
	labelSelector := fmt.Sprintf("review.gemini.google.com/overseer=%s,sandbox.gemini.google.com/type=%s", k8s.TruncateLabel(overseerName), sandboxType)
	listOptions := metav1.ListOptions{
		LabelSelector: labelSelector,
	}
	sandboxList, err := dynClient.Resource(k8s.SandboxGVR).Namespace(namespace).List(ctx, listOptions)
	if err != nil {
		return 0, err
	}
	activeCount := 0
	for _, item := range sandboxList.Items {
		replicas, found, err := unstructured.NestedInt64(item.Object, "spec", "replicas")
		if err == nil && found && replicas == 0 {
			continue
		}
		activeCount++
	}
	return activeCount, nil
}

func getOverseer(ctx context.Context, dynClient dynamic.Interface, name string) (*unstructured.Unstructured, error) {
	gvr := schema.GroupVersionResource{
		Group:    "overseer.gemini.google.com",
		Version:  "v1alpha1",
		Resource: "overseers",
	}
	return dynClient.Resource(gvr).Get(ctx, name, metav1.GetOptions{})
}

func getIssueNumber(labels map[string]string, name string, re *regexp.Regexp) int {
	if numStr, ok := labels["issue.gemini.google.com/number"]; ok {
		num, err := strconv.Atoi(numStr)
		if err == nil {
			return num
		}
		klog.V(4).Infof("failed to parse issue number %q: %v", numStr, err)
	}
	// Fallback to name inference
	if re != nil {
		matches := re.FindStringSubmatch(name)
		if len(matches) > 1 {
			num, _ := strconv.Atoi(matches[1])
			return num
		}
	}
	return 0
}

func getPRNumber(labels map[string]string, name string, re *regexp.Regexp) int {
	if numStr, ok := labels["pr.gemini.google.com/number"]; ok {
		num, err := strconv.Atoi(numStr)
		if err == nil {
			return num
		}
		klog.V(4).Infof("failed to parse PR number %q: %v", numStr, err)
	}
	// Fallback to name inference
	if re != nil {
		matches := re.FindStringSubmatch(name)
		if len(matches) > 1 {
			num, _ := strconv.Atoi(matches[1])
			return num
		}
	}
	return 0
}

func runReconcile(ctx context.Context) error {
	if overseerName == "" || namespace == "" {
		return fmt.Errorf("OVERSEER_NAME and NAMESPACE environment variables must be set")
	}

	choresMode := getMode("CHORES_MODE")
	issueMode := getMode("ISSUE_MODE")
	prMode := getMode("PR_MODE")
	reviewMode := getMode("REVIEW_MODE")

	klog.V(2).Infof("Modes status: CHORES_MODE=%s, ISSUE_MODE=%s, PR_MODE=%s, REVIEW_MODE=%s", choresMode, issueMode, prMode, reviewMode)

	cfg, err := config.GetConfig()
	if err != nil {
		return fmt.Errorf("unable to get kubeconfig: %w", err)
	}

	dynClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("unable to create dynamic client: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("unable to create clientset: %w", err)
	}

	kubeClient := &clients.KubernetesClient{
		DynamicClient: dynClient,
		Clientset:     clientset,
	}

	rwUnstructured, err := getOverseer(ctx, kubeClient.DynamicClient, overseerName)
	if err != nil {
		return fmt.Errorf("failed to get Overseer %s: %w", overseerName, err)
	}

	var overseer overseerv1alpha1.Overseer
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(rwUnstructured.Object, &overseer); err != nil {
		return fmt.Errorf("failed to convert Overseer: %w", err)
	}

	owner, repo, err := parseRepoURL(overseer.Spec.RepoURL)
	var ghClient *github.Client
	if err == nil && owner != "" && repo != "" {
		// Only create GitHub client if we might need it for individual status checks
		if issueMode != "disabled" || prMode != "disabled" || reviewMode != "disabled" {
			ghClient, _ = github.NewClient(ctx)
		}
	}

	// 1. Get current chores in .agents/
	currentChores := make(map[string]bool)
	pausedChores := make(map[string]bool)
	choresReadSuccessful := false
	if choresMode != "disabled" {
		err := filepath.WalkDir(".agents", func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}

			// Skip directories
			if d.IsDir() {
				return nil
			}

			// Also skip symlinks that point to directories
			if d.Type()&os.ModeSymlink != 0 {
				info, err := os.Stat(path)
				if err != nil {
					klog.V(4).Infof("Skipping symlink %s: failed to stat: %v", path, err)
					return nil
				}
				if info.IsDir() {
					return nil
				}
			}

			lowerName := strings.ToLower(d.Name())
			if strings.HasSuffix(lowerName, ".yaml") || strings.HasSuffix(lowerName, ".yml") {
				chore, err := parseChore(path)
				if err != nil {
					// If it's a file without frontmatter, it might just be documentation.
					if errors.Is(err, ErrMissingFrontmatter) {
						klog.V(4).Infof("Skipping non-chore file %s: %v", path, err)
						return nil
					}
					return fmt.Errorf("failed to parse chore file %s: %w", path, err)
				}
				if chore.Name != "" {
					if isChoreAllowed(overseer.Spec.Chores, chore.Name) {
						slug := k8s.TruncateLabel(k8s.Slugify(chore.Name))
						if strings.EqualFold(chore.Schedule, "never") {
							pausedChores[slug] = true
						} else {
							currentChores[slug] = true
						}
					}
				}
			}
			return nil
		})

		if err == nil {
			choresReadSuccessful = true
		} else if os.IsNotExist(err) {
			// If .agents doesn't exist, check if we are in the repo root to prevent accidental mass deletion
			repoMarkers := []string{".git", "go.mod", "Makefile", "package.json", "requirements.txt"}
			foundMarker := ""
			for _, marker := range repoMarkers {
				if _, errRepo := os.Stat(marker); errRepo == nil {
					foundMarker = marker
					break
				}
			}

			if foundMarker != "" {
				choresReadSuccessful = true
				klog.V(4).Infof(".agents directory does not exist, but found repository marker %s. Assuming zero chores.", foundMarker)
			} else {
				klog.Warningf(".agents directory does not exist and no repository marker (%s) found in current directory. Chore cleanup will be skipped to prevent accidental mass deletion.", strings.Join(repoMarkers, ", "))
			}
		} else {
			klog.Warningf("failed to walk .agents directory: %v. Chore cleanup will be skipped.", err)
		}
	} else {
		// If chores are disabled, we don't want to delete based on currentChores map anyway
		choresReadSuccessful = true
	}

	// 2. List all sandboxes for this overseer
	labelSelector := fmt.Sprintf("review.gemini.google.com/overseer=%s", k8s.TruncateLabel(overseer.Name))
	listOptions := metav1.ListOptions{
		LabelSelector: labelSelector,
	}
	sandboxList, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).List(ctx, listOptions)
	if err != nil {
		return fmt.Errorf("failed to list sandboxes: %w", err)
	}

	// 3. Pre-compile regexes for name inference
	issueRe := regexp.MustCompile(fmt.Sprintf(`^%s-issue-(\d+)`, regexp.QuoteMeta(overseer.Name)))
	prRe := regexp.MustCompile(fmt.Sprintf(`^%s-pr-(\d+)`, regexp.QuoteMeta(overseer.Name)))

	// 4. Reconcile sandboxes
	skippedCount := 0
	var reconcileErrs []error
	issueStatusCache := make(map[int]bool)
	prStatusCache := make(map[int]bool)

	for _, item := range sandboxList.Items {
		labels := item.GetLabels()
		if labels == nil {
			labels = make(map[string]string)
		}
		sandboxType, found := labels["sandbox.gemini.google.com/type"]

		if !found {
			klog.V(4).Infof("Sandbox %s lacks type label. Falling back to name inference.", item.GetName())
			// Try to infer type from name for legacy sandboxes
			name := item.GetName()
			if strings.HasPrefix(name, "chore-"+overseer.Name+"-") {
				sandboxType = "chore"
			} else if strings.HasPrefix(name, overseer.Name+"-issue-") {
				sandboxType = "issue"
			} else if strings.HasPrefix(name, overseer.Name+"-pr-") {
				sandboxType = "review"
			} else {
				skippedCount++
				continue
			}
		}

		deleteReason := ""
		isDryRun := dryRun
		switch sandboxType {
		case "chore":
			if !choresReadSuccessful {
				continue
			}

			if choresMode == "disabled" {
				deleteReason = "chores are disabled"
			} else {
				if choresMode == "dryrun" {
					isDryRun = true
				}
				choreSlug, found := labels["chore.gemini.google.com/name"]
				if !found {
					klog.V(4).Infof("Sandbox %s lacks chore name label. Falling back to name inference.", item.GetName())
					// Infer slug from name: chore-<overseer>-<slug>
					prefix := fmt.Sprintf("chore-%s-", overseer.Name)
					if strings.HasPrefix(item.GetName(), prefix) {
						choreSlug = strings.TrimPrefix(item.GetName(), prefix)
						if choreSlug != "" {
							found = true
						}
					}
				}

				if found && !currentChores[k8s.TruncateLabel(k8s.Slugify(choreSlug))] {
					deleteReason = "chore is no longer present or is excluded"
					if pausedChores[k8s.TruncateLabel(k8s.Slugify(choreSlug))] {
						deleteReason = "paused (schedule: never)"
					}
				} else if !found {
					deleteReason = "chore name could not be determined"
				}
			}
		case "issue":
			if issueMode == "disabled" {
				deleteReason = "issue handling is disabled"
			} else {
				if issueMode == "dryrun" {
					isDryRun = true
				}
				if ghClient != nil {
					num := getIssueNumber(labels, item.GetName(), issueRe)
					if num > 0 {
						open, cached := issueStatusCache[num]
						if !cached {
							issue, _, err := ghClient.Issues.Get(ctx, owner, repo, num)
							if err == nil {
								open = issue.GetState() == "open"
								issueStatusCache[num] = open
							} else if github.IsNotFound(err) {
								open = false
								issueStatusCache[num] = open
							} else {
								klog.V(4).Infof("failed to check status for issue %d: %v", num, err)
								open = true // assume open on error
							}
						}
						if !open {
							deleteReason = "issue is closed"
						}
					}
				}
			}
		case "review":
			if reviewMode == "disabled" && prMode == "disabled" {
				deleteReason = "both PR and Review handling are disabled"
			} else {
				// Combined dry-run logic for shared sandbox type: if either mode is enabled,
				// mutations are allowed (subject to global dryRun).
				isDryRun = dryRun || (reviewMode != "enabled" && prMode != "enabled")

				if ghClient != nil {
					num := getPRNumber(labels, item.GetName(), prRe)
					if num > 0 {
						open, cached := prStatusCache[num]
						if !cached {
							pr, _, err := ghClient.PullRequests.Get(ctx, owner, repo, num)
							if err == nil {
								open = pr.GetState() == "open"
								prStatusCache[num] = open
							} else if github.IsNotFound(err) {
								open = false
								prStatusCache[num] = open
							} else {
								klog.V(4).Infof("failed to check status for PR %d: %v", num, err)
								open = true // assume open on error
							}
						}
						if !open {
							deleteReason = "PR is closed or merged"
						}
					}
				}
			}
		default:
			klog.V(4).Infof("Sandbox %s has unrecognized type %q. Skipping.", item.GetName(), sandboxType)
			skippedCount++
			continue
		}

		if deleteReason != "" {
			if isDryRun {
				klog.Infof("[dryrun] Sandbox %s (%s) because %s. Would delete sandbox and its associated resources.", item.GetName(), sandboxType, deleteReason)
			} else {
				klog.Infof("Sandbox %s (%s) because %s. Deleting.", item.GetName(), sandboxType, deleteReason)
				if err := deleteSandbox(ctx, kubeClient, namespace, item.GetName()); err != nil {
					klog.Warningf("failed to clean up resources for sandbox %s: %v", item.GetName(), err)
					reconcileErrs = append(reconcileErrs, fmt.Errorf("cleanup of sandbox %s failed: %w", item.GetName(), err))
				}
			}
		}
	}

	if skippedCount > 0 {
		klog.Warningf("Skipped %d sandboxes whose type could not be determined or were unrecognized.", skippedCount)
	}

	if !choresReadSuccessful && choresMode != "disabled" {
		reconcileErrs = append(reconcileErrs, fmt.Errorf("chore reconciliation was incomplete due to directory read or parsing errors"))
	}

	klog.V(2).Infof("Reconciliation complete.")
	if len(reconcileErrs) > 0 {
		return fmt.Errorf("reconciliation encountered %d errors: %w", len(reconcileErrs), errors.Join(reconcileErrs...))
	}
	return nil
}

func isChoreAllowed(spec *overseerv1alpha1.ChoresSpec, name string) bool {
	if spec == nil {
		return true
	}
	if len(spec.Exclude) > 0 {
		for _, e := range spec.Exclude {
			if e == name {
				return false
			}
		}
	}
	if len(spec.Include) > 0 {
		for _, i := range spec.Include {
			if i == name {
				return true
			}
		}
		return false
	}
	return true
}

func runDeleteSandboxes(ctx context.Context, sandboxNames []string) error {
	if len(sandboxNames) == 0 {
		return fmt.Errorf("at least one sandbox name is required")
	}
	if namespace == "" {
		return fmt.Errorf("NAMESPACE environment variable must be set")
	}

	cfg, err := config.GetConfig()
	if err != nil {
		return fmt.Errorf("unable to get kubeconfig: %w", err)
	}

	dynClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("unable to create dynamic client: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("unable to create clientset: %w", err)
	}

	kubeClient := &clients.KubernetesClient{
		DynamicClient: dynClient,
		Clientset:     clientset,
	}

	// Deduplicate sandbox names
	uniqueNames := make(map[string]bool)
	var deduplicatedNames []string
	for _, name := range sandboxNames {
		name = strings.ToLower(strings.TrimSpace(name))
		if name != "" && !uniqueNames[name] {
			uniqueNames[name] = true
			deduplicatedNames = append(deduplicatedNames, name)
		}
	}

	if len(deduplicatedNames) == 0 {
		return fmt.Errorf("no valid sandbox names provided")
	}

	var errs []error
	for _, sandboxName := range deduplicatedNames {
		if dryRun {
			// Fetch sandbox first during dry run to provide better simulation
			_, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Get(ctx, sandboxName, metav1.GetOptions{})
			if err != nil {
				if kerrors.IsNotFound(err) {
					klog.Warningf("[dryrun] Sandbox %s not found in cluster. Skipping.", sandboxName)
				} else {
					klog.Warningf("[dryrun] Failed to check for sandbox %s: %v", sandboxName, err)
				}
				continue
			}
			klog.Infof("[dryrun] Would delete sandbox %s and associated resources", sandboxName)
			continue
		}

		klog.Infof("Deleting sandbox %s...", sandboxName)
		if err := deleteSandbox(ctx, kubeClient, namespace, sandboxName); err != nil {
			klog.Warningf("cleanup failed for sandbox %s: %v", sandboxName, err)
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("delete command encountered %d errors: %w", len(errs), errors.Join(errs...))
	}
	return nil
}

// deleteSandbox deletes a sandbox resource and its associated services.
// It first attempts to delete the sandbox itself, then finds and deletes services
// matching the 'sandbox=<name>' label, and finally falls back to a name-based
// deletion for the '<name>-lb' service to ensure legacy resources are cleaned up.
func deleteSandbox(ctx context.Context, kubeClient *clients.KubernetesClient, namespace, sandboxName string) error {
	if strings.TrimSpace(sandboxName) == "" {
		return fmt.Errorf("sandbox name cannot be empty")
	}

	var errs []error
	propagationPolicy := metav1.DeletePropagationBackground
	err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Delete(ctx, sandboxName, metav1.DeleteOptions{
		PropagationPolicy: &propagationPolicy,
	})
	if err != nil && !kerrors.IsNotFound(err) {
		errs = append(errs, fmt.Errorf("failed to delete sandbox %s: %w", sandboxName, err))
	}

	// Also delete associated SandboxTasks
	taskGVR := schema.GroupVersionResource{
		Group:    "custom.agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "sandboxtasks",
	}

	taskSelectors := []string{
		"sandbox.gemini.google.com/sandbox-name=" + k8s.TruncateLabel(sandboxName),
		"sandbox.gemini.google.com/name=" + k8s.TruncateLabel(sandboxName),
		"sandbox=" + k8s.TruncateLabel(sandboxName),
	}
	// Fallback to raw label only if it is a valid Kubernetes label value
	if errsValid := kvalidation.IsValidLabelValue(sandboxName); len(errsValid) == 0 {
		taskSelectors = append(taskSelectors, "sandbox="+sandboxName)
	}

	deletedTaskCount := 0
	seenTasks := make(map[string]bool)

	for _, selector := range taskSelectors {
		taskList, err := kubeClient.DynamicClient.Resource(taskGVR).Namespace(namespace).List(ctx, metav1.ListOptions{
			LabelSelector: selector,
		})
		if err == nil {
			for _, task := range taskList.Items {
				if seenTasks[task.GetName()] {
					continue
				}
				seenTasks[task.GetName()] = true
				err = kubeClient.DynamicClient.Resource(taskGVR).Namespace(namespace).Delete(ctx, task.GetName(), metav1.DeleteOptions{
					PropagationPolicy: &propagationPolicy,
				})
				if err == nil || kerrors.IsNotFound(err) {
					deletedTaskCount++
				} else {
					errs = append(errs, fmt.Errorf("failed to delete SandboxTask %s: %w", task.GetName(), err))
				}
			}
		} else if !kerrors.IsNotFound(err) {
			errs = append(errs, fmt.Errorf("failed to list SandboxTasks for selector %s: %w", selector, err))
		}
	}
	if deletedTaskCount > 0 {
		klog.Infof("Deleted %d associated SandboxTask(s) for sandbox %s.", deletedTaskCount, sandboxName)
	}

	deletedServices := make(map[string]bool)

	// Also delete service if it exists. We search by labels for robustness.
	// We try both new and old labels.
	selectors := []string{
		"sandbox.gemini.google.com/name=" + k8s.TruncateLabel(sandboxName),
		"sandbox.gemini.google.com/sandbox-name=" + k8s.TruncateLabel(sandboxName),
		"sandbox=" + k8s.TruncateLabel(sandboxName),
	}
	if errsValid := kvalidation.IsValidLabelValue(sandboxName); len(errsValid) == 0 {
		selectors = append(selectors, "sandbox="+sandboxName)
	}

	// Deduplicate selectors to avoid redundant API calls
	uniqueSelectors := make([]string, 0, len(selectors))
	seenSelectors := make(map[string]bool)
	for _, s := range selectors {
		if !seenSelectors[s] {
			seenSelectors[s] = true
			uniqueSelectors = append(uniqueSelectors, s)
		}
	}

	for _, selector := range uniqueSelectors {
		listOptions := metav1.ListOptions{
			LabelSelector: selector,
		}
		services, err := kubeClient.Clientset.CoreV1().Services(namespace).List(ctx, listOptions)
		if err == nil {
			for _, svc := range services.Items {
				if deletedServices[svc.Name] {
					continue
				}
				deletedServices[svc.Name] = true // Track attempted deletion
				klog.Infof("Deleting service %s...", svc.Name)
				err = kubeClient.Clientset.CoreV1().Services(namespace).Delete(ctx, svc.Name, metav1.DeleteOptions{
					PropagationPolicy: &propagationPolicy,
				})
				if err != nil && !kerrors.IsNotFound(err) {
					errs = append(errs, fmt.Errorf("failed to delete service %s: %w", svc.Name, err))
				}
			}
		} else if !kerrors.IsNotFound(err) {
			errs = append(errs, fmt.Errorf("failed to list services for selector %s: %w", selector, err))
		}
	}

	// Fallback to name-based deletion for services that might lack labels (legacy)
	serviceName := k8s.TruncateName(sandboxName + "-lb")
	if !deletedServices[serviceName] {
		deletedServices[serviceName] = true
		err = kubeClient.Clientset.CoreV1().Services(namespace).Delete(ctx, serviceName, metav1.DeleteOptions{
			PropagationPolicy: &propagationPolicy,
		})
		if err == nil {
			klog.Infof("Deleted legacy service %s.", serviceName)
		} else if !kerrors.IsNotFound(err) {
			errs = append(errs, fmt.Errorf("failed to delete legacy service %s: %w", serviceName, err))
		}
	}

	// Also delete associated API key secret if it exists
	secretName := sandboxName + "-api-key"
	err = kubeClient.Clientset.CoreV1().Secrets(namespace).Delete(ctx, secretName, metav1.DeleteOptions{
		PropagationPolicy: &propagationPolicy,
	})
	if err == nil {
		klog.Infof("Deleted associated API key secret %s.", secretName)
	} else if !kerrors.IsNotFound(err) {
		errs = append(errs, fmt.Errorf("failed to delete associated secret %s: %w", secretName, err))
	}

	if len(errs) > 0 {
		return fmt.Errorf("cleanup of sandbox %s failed: %w", sandboxName, errors.Join(errs...))
	}
	return nil
}

// getMode returns the normalized mode (enabled, disabled, or dryrun) from an environment variable.
// It handles case-insensitivity, trims whitespace and quotes, and supports boolean
// equivalents (true/false, on/off, etc.) and various dry-run spellings.
func getMode(name string) string {
	val := os.Getenv(name)
	m := strings.ToLower(strings.Trim(val, " \t\n\r\"'"))
	m = strings.TrimSpace(m)
	switch m {
	case "enabled", "enable", "true", "1", "yes", "on", "t", "y":
		return "enabled"
	case "disabled", "disable", "none", "false", "0", "no", "off", "f", "n":
		return "disabled"
	case "dryrun", "dry-run", "dry_run", "dry run":
		return "dryrun"
	case "":
		return "enabled"
	default:
		// Truncate the unrecognized value to avoid log bloating
		displayVal := m
		if len(displayVal) > 50 {
			displayVal = displayVal[:47] + "..."
		}
		klog.Warningf("unrecognized mode %q for environment variable %s. Defaulting to \"enabled\" for safety. Valid modes are: enabled, disabled, dryrun.", displayVal, name)
		return "enabled"
	}
}

func getTokenFromScript() (string, error) {
	dir := os.Getenv("TOKENSCRIPT_DIR")
	if dir == "" {
		return "", nil
	}

	files, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("failed to read tokenscript dir: %w", err)
	}

	for _, f := range files {
		if f.IsDir() || strings.HasPrefix(f.Name(), "..") {
			continue
		}

		path := filepath.Join(dir, f.Name())
		cmd := exec.Command(path)
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("failed to run tokenscript %s: %w", path, err)
		}

		return strings.TrimSpace(out.String()), nil
	}

	return "", nil
}
