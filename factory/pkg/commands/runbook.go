package commands

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/constants"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/envd"
	factorysandbox "github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/sandbox"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/tasks"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/usagereport"
	"github.com/spf13/cobra"
)

// NewRunbookCommand executes a runbook scenario in a dedicated run sandbox.
//
//	factory runbook run      --url <repo> --scenario deploy [--path gke] [--guidance …]
//	factory runbook teardown --url <repo> --scenario deploy [--path gke]
//
// The runbook is the source, the emitted script is the build artifact,
// the receipt is the test result — all on the fork's exploration/notes
// branch. One sandbox per (repo, scenario, path): the PVC holds the
// deployment's living state, so the sandbox is the deployment handle.
func NewRunbookCommand(ctx context.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "runbook",
		Aliases: []string{"try"},
		Short:   "Execute a runbook scenario in a dedicated run sandbox",
	}

	var repoURL, scenario, path, guidance string

	run := func(mode string) func(*cobra.Command, []string) error {
		return func(c *cobra.Command, _ []string) error {
			if _, err := ResolveRootFlags(c); err != nil {
				return err
			}
			if repoURL == "" {
				return fmt.Errorf("--url is required")
			}
			scenario = slugifyScenario(scenario)
			if scenario == "" || scenario == "all" {
				return fmt.Errorf("--scenario must name one runbook scenario (deploy, upgrade, …)")
			}
			path = slugifyScenario(path)
			if rootFlags.Timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, rootFlags.Timeout)
				defer cancel()
			}
			return runRunbook(ctx, mode, repoURL, scenario, path, guidance)
		}
	}

	runCmd := &cobra.Command{
		Use:   "run",
		Short: "Run the scenario (reuses the emitted script when nothing drifted)",
		RunE:  run("run"),
	}
	teardown := &cobra.Command{
		Use:   "teardown",
		Short: "Tear the deployment down (prefers the deterministic teardown script)",
		RunE:  run("teardown"),
	}

	for _, sub := range []*cobra.Command{runCmd, teardown} {
		sub.Flags().StringVar(&repoURL, "url", "", "GitHub repository URL (e.g. https://github.com/owner/repo)")
		sub.Flags().StringVar(&scenario, "scenario", "", "The runbook scenario (deploy, upgrade, …)")
		sub.Flags().StringVar(&path, "path", "", "Target path within the runbook (gke, local, …)")
		cmd.AddCommand(sub)
	}
	runCmd.Flags().StringVar(&guidance, "guidance", "", "Owner constraints for this run (pinned decisions)")

	return cmd
}

func runRunbook(ctx context.Context, mode, repoURL, scenario, path, guidance string) error {
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

	fmt.Printf("Ensuring run sandbox for %s/%s (%s%s)...\n", owner, repo, scenario, suffixOrEmpty(path))
	sandboxName, err := factorysandbox.EnsureRunbookSandbox(ctx, kubeClient, rootFlags.Namespace, repo, scenario, path, cloneURL, htmlURL, rootFlags.Image, rootFlags.DiskSize, rootFlags.EphemeralStorage, rootFlags.ResolvedSecrets, rootFlags.ResolvedEnvs, rootFlags.User)
	if err != nil {
		return fmt.Errorf("ensuring run sandbox: %w", err)
	}

	secret, err := kubeClient.Clientset.CoreV1().Secrets(rootFlags.Namespace).Get(ctx, rootFlags.SecretName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("fetching %s secret in namespace %s: %w (make sure to run 'factory user onboard' first)", rootFlags.SecretName, rootFlags.Namespace, err)
	}
	githubLogin := string(secret.Data[constants.KeyGithubLogin])
	githubEmail := string(secret.Data[constants.KeyGithubEmail])

	promptBytes, err := tasks.RenderRunbookPrompt(mode, tasks.RunbookParams{
		RepoName: repo,
		HTMLURL:  htmlURL,
		Scenario: scenario,
		Path:     path,
		Guidance: guidance,
	})
	if err != nil {
		return fmt.Errorf("rendering runbook prompt: %w", err)
	}
	scriptBytes, err := tasks.GetRunbookScript()
	if err != nil {
		return fmt.Errorf("getting runbook script: %w", err)
	}

	fmt.Printf("Connecting to sandbox %s via envd...\n", sandboxName)
	client, err := envd.Connect(ctx, rootFlags.Namespace, sandboxName)
	if err != nil {
		return fmt.Errorf("connecting to sandbox: %w", err)
	}
	defer client.Close()

	taskDir := fmt.Sprintf("/workspaces/tasks/runbook-%s", time.Now().Format("20060102-150405"))
	promptPath := fmt.Sprintf("%s/agent-prompt.txt", taskDir)
	scriptPath := fmt.Sprintf("%s/pre-script.sh", taskDir)

	fmt.Println("Writing prompt and script into sandbox...")
	if err := client.WriteFile(ctx, promptPath, promptBytes); err != nil {
		return fmt.Errorf("writing prompt: %w", err)
	}
	if err := client.WriteFile(ctx, scriptPath, scriptBytes); err != nil {
		return fmt.Errorf("writing script: %w", err)
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
		"RUNBOOK_SCENARIO":           scenario,
		"RUNBOOK_PATH":               path,
		"RUNBOOK_MODE":               mode,
	}
	// BYO GCP project: Workload Identity supplies credentials via the
	// pod's KSA; the secret only carries where to deploy.
	if p := string(secret.Data[constants.KeyGcpProject]); p != "" {
		envMap["GOOGLE_CLOUD_PROJECT"] = p
		envMap["CLOUDSDK_CORE_PROJECT"] = p
	}
	if r := string(secret.Data[constants.KeyGcpRegion]); r != "" {
		envMap["CLOUDSDK_COMPUTE_REGION"] = r
	}
	if err := applyEngineEnv(envMap, secret); err != nil {
		return err
	}

	cmdStr := fmt.Sprintf("bash -c 'set -o pipefail; bash %s'", scriptPath)
	// Stamp Running at dispatch so the board reads the truth mid-run;
	// the prober corrects a stale Running if this invocation dies.
	_ = factorysandbox.UpdateSandboxTaskAnnotation(ctx, kubeClient, rootFlags.Namespace, sandboxName, "runbook", "Running")
	if err := client.RunTaskResilient(ctx, cmdStr, envMap, taskDir, rootFlags.Detached, rootFlags.AbortOnCancel); err != nil {
		_ = factorysandbox.UpdateSandboxTaskAnnotation(ctx, kubeClient, rootFlags.Namespace, sandboxName, "runbook", "Failed")
		return fmt.Errorf("running runbook task: %w", err)
	}
	if rootFlags.Detached {
		return nil
	}

	usagereport.HarvestTask(ctx, client, taskDir, usagereport.Meta{
		Repo:     owner + "/" + repo,
		TaskType: "runbook",
		Sandbox:  sandboxName,
	})
	_ = factorysandbox.UpdateSandboxTaskAnnotation(ctx, kubeClient, rootFlags.Namespace, sandboxName, "runbook", "Completed")
	fmt.Printf("Runbook %s finished for %s/%s (%s%s); receipt pushed to the exploration/notes branch.\n", mode, owner, repo, scenario, suffixOrEmpty(path))
	return nil
}

func suffixOrEmpty(path string) string {
	if path == "" {
		return ""
	}
	return "/" + path
}
