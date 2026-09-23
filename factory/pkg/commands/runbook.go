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
//	factory runbook run      --url <repo> --scenario deploy-gcp [--instance …] [--guidance …]
//	factory runbook teardown --url <repo> --scenario deploy-gcp [--instance …]
//
// The runbook is the source, the emitted script is the build artifact,
// the receipt is the test result — all on the fork's exploration/notes
// branch. One sandbox per instance: the PVC holds the
// deployment's living state, so the sandbox is the deployment handle.
func NewRunbookCommand(ctx context.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "runbook",
		Aliases: []string{"try"},
		Short:   "Execute a runbook scenario in a dedicated run sandbox",
	}

	var repoURL, scenario, instance, guidance string

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
			instance = slugifyScenario(instance)
			if instance == "" {
				instance = scenario
			}
			// Runbook runs build images; the 6Gi ephemeral default is
			// sized for code tasks. 10Gi is the GKE Autopilot per-pod
			// ceiling — only when nothing chose a value explicitly.
			if ephemeralStorageDefaulted {
				rootFlags.EphemeralStorage = "10Gi"
			}
			// Go build+module caches for a big monorepo alone run
			// ~7.5Gi on the PVC; 10Gi leaves no room for the repo,
			// images and artifacts.
			if workspaceDiskDefaulted {
				rootFlags.DiskSize = "30Gi"
			}
			if rootFlags.Timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, rootFlags.Timeout)
				defer cancel()
			}
			return runRunbook(ctx, mode, repoURL, scenario, instance, guidance)
		}
	}

	runCmd := &cobra.Command{
		Use:   "run",
		Short: "Plan and execute in one pass (automation; the UI uses plan then deploy)",
		RunE:  run("run"),
	}
	planCmd := &cobra.Command{
		Use:   "plan",
		Short: "Prepare only: scripts + a PLANNED receipt pushed for review, nothing executed",
		RunE:  run("plan"),
	}
	deployCmd := &cobra.Command{
		Use:   "deploy",
		Short: "Execute a reviewed plan (runs the instance's pushed deploy.sh)",
		RunE:  run("deploy"),
	}
	teardown := &cobra.Command{
		Use:   "teardown",
		Short: "Tear the deployment down (prefers the deterministic teardown script)",
		RunE:  run("teardown"),
	}

	for _, sub := range []*cobra.Command{runCmd, planCmd, deployCmd, teardown} {
		sub.Flags().StringVar(&repoURL, "url", "", "GitHub repository URL (e.g. https://github.com/owner/repo)")
		sub.Flags().StringVar(&scenario, "scenario", "", "The runbook name (deploy-gcp, upgrade-gcp, …)")
		sub.Flags().StringVar(&instance, "instance", "", "Deployment instance name (default: the runbook name); one runbook, many parameterized deployments")
		cmd.AddCommand(sub)
	}
	runCmd.Flags().StringVar(&guidance, "guidance", "", "Owner constraints for this run (pinned decisions)")
	planCmd.Flags().StringVar(&guidance, "guidance", "", "Owner constraints for this plan (pinned decisions)")

	return cmd
}

// shortName compresses a repo name for resource prefixes: initials of
// hyphenated words (in-cluster-storage → ics), else the name truncated.
func shortName(repo string) string {
	slug := slugifyScenario(repo)
	words := strings.Split(slug, "-")
	if len(words) >= 2 {
		var b strings.Builder
		for _, w := range words {
			if w != "" {
				b.WriteByte(w[0])
			}
		}
		return b.String()
	}
	if len(slug) > 8 {
		return slug[:8]
	}
	return slug
}

// resourcePrefix is the project-unique identity for everything a run
// creates: shortName(repo)-instance. Deliberately dumb — no stutter
// collapsing (it would be renaming the user can't control, and it can
// alias two instances onto one prefix). Stutter is prevented at the
// source: the UI shows the prefix so nobody hand-types it.
func resourcePrefix(repo, instance string) string {
	return shortName(repo) + "-" + instance
}

func runRunbook(ctx context.Context, mode, repoURL, scenario, instance, guidance string) error {
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

	fmt.Printf("Ensuring run sandbox for %s/%s (instance %s)...\n", owner, repo, instance)
	sandboxName, err := factorysandbox.EnsureRunbookSandbox(ctx, kubeClient, rootFlags.Namespace, repo, scenario, instance, cloneURL, htmlURL, rootFlags.Image, rootFlags.DiskSize, rootFlags.EphemeralStorage, rootFlags.ResolvedSecrets, rootFlags.ResolvedEnvs, rootFlags.User)
	if err != nil {
		return fmt.Errorf("ensuring run sandbox: %w", err)
	}

	secret, err := kubeClient.Clientset.CoreV1().Secrets(rootFlags.Namespace).Get(ctx, rootFlags.SecretName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("fetching %s secret in namespace %s: %w (make sure to run 'factory user onboard' first)", rootFlags.SecretName, rootFlags.Namespace, err)
	}
	githubLogin := string(secret.Data[constants.KeyGithubLogin])
	githubEmail := string(secret.Data[constants.KeyGithubEmail])

	params := tasks.RunbookParams{
		RepoName: repo,
		HTMLURL:  htmlURL,
		Scenario: scenario,
		Mode:     mode,
		Instance: instance,
		Guidance: guidance,
	}
	phases := map[string][]string{
		"run":      {"prepare", "execute"},
		"plan":     {"prepare"},
		"deploy":   {"execute"},
		"teardown": {"teardown"},
	}[mode]
	if phases == nil {
		return fmt.Errorf("unknown runbook mode %q (plan|deploy|run|teardown)", mode)
	}
	prompts := map[string][]byte{}
	for _, ph := range phases {
		b, perr := tasks.RenderRunbookPrompt(ph, params)
		if perr != nil {
			return fmt.Errorf("rendering runbook %s prompt: %w", ph, perr)
		}
		prompts[ph] = b
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
	scriptPath := fmt.Sprintf("%s/pre-script.sh", taskDir)
	promptPaths := map[string]string{}

	fmt.Println("Writing prompts and script into sandbox...")
	for pm, b := range prompts {
		p := fmt.Sprintf("%s/agent-prompt-%s.txt", taskDir, pm)
		if err := client.WriteFile(ctx, p, b); err != nil {
			return fmt.Errorf("writing %s prompt: %w", pm, err)
		}
		promptPaths[pm] = p
	}
	if err := client.WriteFile(ctx, scriptPath, scriptBytes); err != nil {
		return fmt.Errorf("writing script: %w", err)
	}
	promptPath := promptPaths[phases[0]]

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
		"RUNBOOK_INSTANCE":           instance,
		"RUNBOOK_MODE":               mode,
		"RUNBOOK_RESOURCE_PREFIX":    resourcePrefix(repo, instance),
	}
	if p, ok := promptPaths["prepare"]; ok {
		envMap["PREPARE_PROMPT_FILE"] = p
	}
	if p, ok := promptPaths["execute"]; ok {
		envMap["EXECUTE_PROMPT_FILE"] = p
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
	fmt.Printf("Runbook %s finished for %s/%s (instance %s); receipt pushed to the exploration/notes branch.\n", mode, owner, repo, instance)
	return nil
}
