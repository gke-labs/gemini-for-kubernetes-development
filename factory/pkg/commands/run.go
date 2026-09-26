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

// slugifyScenario makes the scenario safe as a filename under
// runbooks/ (lowercase, dashes, nothing else). A typed ".md" is the
// name of the file, not part of it: "deploy-gcp.md" means the
// existing deploy-gcp run, never a new deploy-gcp-md twin.
func slugifyScenario(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimSuffix(s, ".md")
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:] // a pasted path names its file
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// NewRunCommand plans, deploys or tears down one run.
//
//	factory run plan     --url <repo> --name deploy-gke-k8s1 [--intent …] [--from …]
//	factory run deploy   --url <repo> --name deploy-gke-k8s1
//	factory run teardown --url <repo> --name deploy-gke-k8s1
//
// Plan does the drafting too. It writes the procedure first and the
// scripts from it, and where the parameters cannot be resolved — no
// GCP project configured, a target the owner has not chosen — it
// stops after the procedure and says what it needs. Nothing executes
// at plan time either way, so there was never a reason to offer a
// separate, earlier stopping point.
//
// A run owns everything it needs, in one directory on the fork's
// exploration/notes branch: runbook.md is the procedure, the scripts
// are generated from it, the receipts are the test results. Nothing is
// shared between runs, which is what lets a deploy correct the
// procedure in place instead of filing recommendations against a
// document other runs also read.
//
// This supersedes `factory runbook`, where a runbook was a separate
// shared document and an instance only held the artifacts. That split
// is what sibling-hiding, the read-only guard and the stash-restore
// dance existed to police; none of them are needed here.
func NewRunCommand(ctx context.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Plan, deploy or tear down a run",
	}

	var repoURL, name, intent, from string

	exec := func(mode string) func(*cobra.Command, []string) error {
		return func(c *cobra.Command, _ []string) error {
			if _, err := ResolveRootFlags(c); err != nil {
				return err
			}
			if repoURL == "" {
				return fmt.Errorf("--url is required")
			}
			name = slugifyScenario(name)
			if name == "" {
				return fmt.Errorf("--name is required (the run's identity, e.g. deploy-gke-k8s1)")
			}
			from = slugifyScenario(from)
			if from != "" && mode != "plan" {
				return fmt.Errorf("--from applies to plan only")
			}
			// Runs build images; the 6Gi ephemeral default is sized for
			// code tasks. 10Gi is the GKE Autopilot per-pod ceiling —
			// only when nothing chose a value explicitly.
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
			return runRun(ctx, mode, repoURL, name, intent, from)
		}
	}

	planCmd := &cobra.Command{
		Use:   "plan",
		Short: "Author the procedure and generate its scripts; nothing executes",
		RunE:  exec("plan"),
	}
	deployCmd := &cobra.Command{
		Use:   "deploy",
		Short: "Execute an approved plan, then reconcile runbook.md to what actually worked",
		RunE:  exec("deploy"),
	}
	teardownCmd := &cobra.Command{
		Use:   "teardown",
		Short: "Remove what this run created (prefers the generated teardown script)",
		RunE:  exec("teardown"),
	}

	for _, sub := range []*cobra.Command{planCmd, deployCmd, teardownCmd} {
		sub.Flags().StringVar(&repoURL, "url", "", "GitHub repository URL (e.g. https://github.com/owner/repo)")
		sub.Flags().StringVar(&name, "name", "", "The run's identity and directory (e.g. deploy-gke-k8s1)")
		cmd.AddCommand(sub)
	}
	planCmd.Flags().StringVar(&intent, "intent", "", "What this run should do, or what to change on a re-plan")
	planCmd.Flags().StringVar(&from, "from", "", "Seed the procedure from an existing run's runbook.md")
	teardownCmd.Flags().StringVar(&intent, "intent", "", "Anything the owner wants watched during teardown")

	return cmd
}

func runRun(ctx context.Context, mode, repoURL, name, intent, from string) error {
	u, err := url.Parse(repoURL)
	if err != nil {
		return fmt.Errorf("invalid repo URL: %w", err)
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

	// Sandbox naming is unchanged from the runbook command on purpose:
	// live deployments hold their teardown scripts inside these
	// sandboxes, and renaming would orphan them.
	fmt.Printf("Ensuring run sandbox for %s/%s (run %s)...\n", owner, repo, name)
	sandboxName, err := factorysandbox.EnsureRunbookSandbox(ctx, kubeClient, rootFlags.Namespace, repo, name, name, cloneURL, htmlURL, rootFlags.Image, rootFlags.DiskSize, rootFlags.EphemeralStorage, rootFlags.ResolvedSecrets, rootFlags.ResolvedEnvs, rootFlags.User)
	if err != nil {
		return fmt.Errorf("ensuring run sandbox: %w", err)
	}

	secret, err := kubeClient.Clientset.CoreV1().Secrets(rootFlags.Namespace).Get(ctx, rootFlags.SecretName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("fetching %s secret in namespace %s: %w (make sure to run 'factory user onboard' first)", rootFlags.SecretName, rootFlags.Namespace, err)
	}
	githubLogin := string(secret.Data[constants.KeyGithubLogin])
	githubEmail := string(secret.Data[constants.KeyGithubEmail])

	promptBytes, err := tasks.RenderRunPrompt(mode, tasks.RunParams{
		RepoName: repo,
		HTMLURL:  htmlURL,
		Name:     name,
		Mode:     mode,
		Intent:   intent,
		From:     from,
	})
	if err != nil {
		return fmt.Errorf("rendering run %s prompt: %w", mode, err)
	}
	scriptBytes, err := tasks.GetRunScript()
	if err != nil {
		return fmt.Errorf("getting run script: %w", err)
	}

	fmt.Printf("Connecting to sandbox %s via envd...\n", sandboxName)
	client, err := envd.Connect(ctx, rootFlags.Namespace, sandboxName)
	if err != nil {
		return fmt.Errorf("connecting to sandbox: %w", err)
	}
	defer client.Close()

	taskDir := fmt.Sprintf("/workspaces/tasks/run-%s", time.Now().Format("20060102-150405"))
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
		"RUN_NAME":                   name,
		"RUN_MODE":                   mode,
		"RUN_FROM":                   from,
		"RUN_RESOURCE_PREFIX":        resourcePrefix(repo, name),
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
	_ = factorysandbox.UpdateSandboxTaskAnnotation(ctx, kubeClient, rootFlags.Namespace, sandboxName, "run", "Running")
	if err := client.RunTaskResilient(ctx, cmdStr, envMap, taskDir, rootFlags.Detached, rootFlags.AbortOnCancel); err != nil {
		_ = factorysandbox.UpdateSandboxTaskAnnotation(ctx, kubeClient, rootFlags.Namespace, sandboxName, "run", "Failed")
		return fmt.Errorf("running run task: %w", err)
	}
	if rootFlags.Detached {
		return nil
	}

	usagereport.HarvestTask(ctx, client, taskDir, usagereport.Meta{
		Repo:     owner + "/" + repo,
		TaskType: "run",
		Sandbox:  sandboxName,
	})
	_ = factorysandbox.UpdateSandboxTaskAnnotation(ctx, kubeClient, rootFlags.Namespace, sandboxName, "run", "Completed")
	fmt.Printf("Run %s %s finished for %s/%s; pushed to the exploration/notes branch.\n", name, mode, owner, repo)
	return nil
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
// creates: shortName(repo)-name. Deliberately dumb — no stutter
// collapsing (it would be renaming the user cannot control, and it can
// alias two runs onto one prefix). Stutter is prevented at the source:
// the UI shows the prefix so nobody hand-types it.
func resourcePrefix(repo, name string) string {
	return shortName(repo) + "-" + name
}
