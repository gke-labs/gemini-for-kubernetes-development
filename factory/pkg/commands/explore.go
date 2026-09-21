package commands

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/constants"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/envd"
	factorysandbox "github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/sandbox"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/tasks"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/usagereport"
)

// NewExploreCommand builds and maintains a personal understanding of a
// repository: notes live in docs-exploration/ on the exploration/notes
// branch of the invoking user's FORK (no upstream writes — exploration
// is draft-tier and works without push rights on the repo).
//
// Well-defined jobs are subcommands; the open-ended one takes --topic:
//
//	factory explore onboard  --url <repo>              foundation docs
//	factory explore activity --url <repo> --since "2 weeks"
//	factory explore topic    --url <repo> --topic "compare with Envoy"
func NewExploreCommand(ctx context.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "explore",
		Short: "Build and maintain understanding docs for a repository in your fork",
	}

	var repoURL, topic, since string

	run := func(kind string) func(*cobra.Command, []string) error {
		return func(c *cobra.Command, _ []string) error {
			if _, err := ResolveRootFlags(c); err != nil {
				return err
			}
			if repoURL == "" {
				return fmt.Errorf("--url is required")
			}
			if kind == "topic" && strings.TrimSpace(topic) == "" {
				return fmt.Errorf("--topic is required for `explore topic`")
			}
			if rootFlags.Timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, rootFlags.Timeout)
				defer cancel()
			}
			return runExplore(ctx, kind, repoURL, topic, since)
		}
	}

	onboard := &cobra.Command{
		Use:   "onboard",
		Short: "Establish or refresh the foundation docs (overview, architecture, code map)",
		RunE:  run("onboard"),
	}
	activity := &cobra.Command{
		Use:   "activity",
		Short: "Digest a recent window: themes, churn, notable merges, maintainer asks",
		RunE:  run("activity"),
	}
	topicCmd := &cobra.Command{
		Use:   "topic",
		Short: "Free-form deep dive on a topic (architecture question, comparison, subsystem)",
		RunE:  run("topic"),
	}

	for _, sub := range []*cobra.Command{onboard, activity, topicCmd} {
		sub.Flags().StringVar(&repoURL, "url", "", "GitHub repository URL (e.g. https://github.com/owner/repo)")
		cmd.AddCommand(sub)
	}
	activity.Flags().StringVar(&since, "since", "2 weeks", "Window to digest (e.g. \"2 weeks\", \"1 month\")")
	topicCmd.Flags().StringVar(&topic, "topic", "", "The question or comparison to investigate")

	return cmd
}

func runExplore(ctx context.Context, kind, repoURL, topic, since string) error {
	u, err := url.Parse(repoURL)
	if err != nil {
		return fmt.Errorf("invalid repository URL: %w", err)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 {
		return fmt.Errorf("expected URL format https://github.com/owner/repo, got %s", repoURL)
	}
	owner, repo := parts[0], strings.TrimSuffix(parts[1], ".git")
	cloneURL := fmt.Sprintf("https://github.com/%s/%s.git", owner, repo)
	htmlURL := fmt.Sprintf("https://github.com/%s/%s", owner, repo)

	kubeClient, err := clients.NewKubernetesClient()
	if err != nil {
		return fmt.Errorf("creating k8s client: %w", err)
	}

	fmt.Printf("Ensuring exploration sandbox for %s/%s...\n", owner, repo)
	sandboxName, err := factorysandbox.EnsureExploreSandbox(ctx, kubeClient, rootFlags.Namespace, repo, cloneURL, htmlURL, rootFlags.Image, rootFlags.DiskSize, rootFlags.EphemeralStorage, rootFlags.ResolvedSecrets, rootFlags.ResolvedEnvs, rootFlags.User)
	if err != nil {
		return fmt.Errorf("ensuring exploration sandbox: %w", err)
	}

	secret, err := kubeClient.Clientset.CoreV1().Secrets(rootFlags.Namespace).Get(ctx, rootFlags.SecretName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("fetching %s secret in namespace %s: %w (make sure to run 'factory user onboard' first)", rootFlags.SecretName, rootFlags.Namespace, err)
	}
	githubLogin := string(secret.Data[constants.KeyGithubLogin])
	githubEmail := string(secret.Data[constants.KeyGithubEmail])

	promptBytes, err := tasks.RenderExplorePrompt(kind, tasks.ExploreParams{
		RepoName: repo,
		HTMLURL:  htmlURL,
		Topic:    topic,
		Since:    since,
	})
	if err != nil {
		return fmt.Errorf("rendering exploration prompt: %w", err)
	}
	scriptBytes, err := tasks.GetExploreScript()
	if err != nil {
		return fmt.Errorf("getting exploration script: %w", err)
	}

	fmt.Printf("Connecting to sandbox %s via envd...\n", sandboxName)
	client, err := envd.Connect(ctx, rootFlags.Namespace, sandboxName)
	if err != nil {
		return fmt.Errorf("connecting to sandbox: %w", err)
	}
	defer client.Close()

	taskDir := fmt.Sprintf("/workspaces/tasks/explore-%s", time.Now().Format("20060102-150405"))
	promptPath := fmt.Sprintf("%s/agent-prompt.txt", taskDir)
	scriptPath := fmt.Sprintf("%s/pre-script.sh", taskDir)
	skillPath := fmt.Sprintf("%s/SKILL.md", taskDir)

	fmt.Println("Writing prompt, script and skill into sandbox...")
	if err := client.WriteFile(ctx, promptPath, promptBytes); err != nil {
		return fmt.Errorf("writing prompt: %w", err)
	}
	if err := client.WriteFile(ctx, scriptPath, scriptBytes); err != nil {
		return fmt.Errorf("writing script: %w", err)
	}
	if err := client.WriteFile(ctx, skillPath, tasks.GetExploreSkill()); err != nil {
		return fmt.Errorf("writing skill: %w", err)
	}

	envMap := map[string]string{
		"HOME":                       "/workspaces/.home",
		"GITHUB_TOKEN":               string(secret.Data[constants.KeyGithubToken]),
		"GEMINI_CLI_TRUST_WORKSPACE": "true",
		"REPO_NAME":                  repo,
		"CLONE_URL":                  cloneURL,
		"PROMPT_FILE":                promptPath,
		"GITHUB_USER_ID":             githubLogin,
		"GITHUB_USER_EMAIL":          githubEmail,
		"GITHUB_USER_NAME":           githubLogin,
		"EXPLORE_KIND":               kind,
	}
	if err := applyEngineEnv(envMap, secret); err != nil {
		return err
	}

	fmt.Printf("Running explore %s task via envd...\n", kind)
	cmdStr := fmt.Sprintf("bash -c 'set -o pipefail; bash %s'", scriptPath)
	_ = factorysandbox.UpdateSandboxTaskAnnotation(ctx, kubeClient, rootFlags.Namespace, sandboxName, "explore", "Running")
	if err := client.RunTaskResilient(ctx, cmdStr, envMap, taskDir, rootFlags.Detached, rootFlags.AbortOnCancel); err != nil {
		_ = factorysandbox.UpdateSandboxTaskAnnotation(ctx, kubeClient, rootFlags.Namespace, sandboxName, "explore", "Failed")
		return fmt.Errorf("running task: %w", err)
	}
	if rootFlags.Detached {
		// The reattach path (or the runner preflight) settles the final
		// state; stamping Completed here would lie mid-run.
		return nil
	}
	usagereport.HarvestTask(ctx, client, taskDir, usagereport.Meta{
		Repo:     owner + "/" + repo,
		TaskType: "explore",
		Sandbox:  sandboxName,
	})
	_ = factorysandbox.UpdateSandboxTaskAnnotation(ctx, kubeClient, rootFlags.Namespace, sandboxName, "explore", "Completed")

	fmt.Printf("Exploration notes updated on the exploration/notes branch of %s's fork of %s/%s.\n", githubLogin, owner, repo)
	return nil
}
