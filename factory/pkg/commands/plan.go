package commands

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/constants"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/envd"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
	factorysandbox "github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/sandbox"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/tasks"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/usagereport"
)

type PlanFlags struct {
	IssueURL     string
	Feedback     string
	Instructions []string
}

// NewPlanCommand prepares an implementation plan for a GitHub issue in a
// sandbox. Nothing is written to GitHub and no code is changed: the plan is
// written to /workspaces/plan-issue-<n>.md inside the sandbox (where a later
// `factory fix --with-plan` picks it up) and printed between the ISSUE PLAN
// banners for the caller. The plan runs in the issue's fix sandbox, so the
// exploration that produced the plan happens in the same checkout the fix
// will run in.
//
// Refinement: re-running with --feedback revises the previous plan (found in
// the sandbox) against the maintainer's feedback instead of starting over.
func NewPlanCommand(ctx context.Context) *cobra.Command {
	var flags PlanFlags

	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Prepare an implementation plan for a GitHub issue in a sandbox pod",
		Example: `  # Draft an implementation plan (printed, nothing posted, no code changed)
  factory plan --url https://github.com/owner/repo/issues/123

  # Revise the previous plan against maintainer feedback
  factory plan --url https://github.com/owner/repo/issues/123 --feedback "steps 2 and 3 should be one migration; don't touch the v1 API"`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := ResolveRootFlags(cmd)
			if err != nil {
				return err
			}
			if flags.IssueURL == "" {
				return fmt.Errorf("--url is required")
			}

			if rootFlags.Timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, rootFlags.Timeout)
				defer cancel()
			}
			return runPlan(ctx, flags, rootFlags.EphemeralStorage, rootFlags.ResolvedSecrets)
		},
	}

	cmd.Flags().StringVar(&flags.IssueURL, "url", "", "GitHub issue URL (e.g. https://github.com/owner/repo/issues/123)")
	cmd.Flags().StringVar(&flags.Feedback, "feedback", "", "Maintainer feedback: revise the previous plan in the sandbox against this feedback")
	cmd.Flags().StringSliceVar(&flags.Instructions, "instruction", nil, "Planning instruction (local file path, repo file path, or raw string). Can be specified multiple times.")

	return cmd
}

func runPlan(ctx context.Context, flags PlanFlags, ephemeralStorage string, secrets []factorysandbox.SecretMount) error {
	fmt.Printf("Resolving issue URL: %s...\n", flags.IssueURL)

	u, err := url.Parse(flags.IssueURL)
	if err != nil {
		return fmt.Errorf("invalid issue URL: %w", err)
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(parts) < 4 || parts[2] != "issues" {
		return fmt.Errorf("expected URL format https://github.com/owner/repo/issues/123, got %s", flags.IssueURL)
	}
	owner, repo := parts[0], parts[1]
	issueNum, err := strconv.Atoi(parts[3])
	if err != nil {
		return fmt.Errorf("invalid issue number in URL: %s", parts[3])
	}

	ghClient, err := github.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("creating github client: %w", err)
	}
	issue, _, err := ghClient.Issues.Get(ctx, owner, repo, issueNum)
	if err != nil {
		return fmt.Errorf("fetching github issue #%d: %w", issueNum, err)
	}

	var instructions []string
	for _, instVal := range flags.Instructions {
		content, isFile, err := resolveInstruction(ctx, ghClient, owner, repo, "", instVal)
		if err != nil {
			return err
		}
		if isFile {
			fmt.Printf("Loaded instruction file: %s\n", instVal)
		} else {
			fmt.Printf("Loaded instruction: %q\n", instVal)
		}
		instructions = append(instructions, content)
	}

	kubeClient, err := clients.NewKubernetesClient()
	if err != nil {
		return fmt.Errorf("creating k8s client: %w", err)
	}

	cloneURL := fmt.Sprintf("https://github.com/%s/%s.git", owner, repo)
	// The plan runs in the issue's FIX sandbox: planning and fixing share
	// one checkout, so the approved plan is already sitting next to the
	// code when the fix launches.
	fmt.Printf("Ensuring fix sandbox for issue #%d...\n", issueNum)
	sandboxName, err := factorysandbox.EnsureFixSandbox(ctx, kubeClient, rootFlags.Namespace, repo, strconv.Itoa(issueNum), cloneURL, issue.GetHTMLURL(), issue.GetTitle(), rootFlags.Image, rootFlags.DiskSize, ephemeralStorage, secrets, rootFlags.ResolvedEnvs, rootFlags.User)
	if err != nil {
		return fmt.Errorf("ensuring fix sandbox: %w", err)
	}

	secret, err := kubeClient.Clientset.CoreV1().Secrets(rootFlags.Namespace).Get(ctx, rootFlags.SecretName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("fetching %s secret in namespace %s: %w (make sure to run 'factory user onboard' first)", rootFlags.SecretName, rootFlags.Namespace, err)
	}
	githubLogin := string(secret.Data[constants.KeyGithubLogin])
	githubEmail := string(secret.Data[constants.KeyGithubEmail])

	fmt.Printf("Connecting to sandbox %s via envd...\n", sandboxName)
	client, err := envd.Connect(ctx, rootFlags.Namespace, sandboxName)
	if err != nil {
		return fmt.Errorf("connecting to sandbox: %w", err)
	}
	defer client.Close()

	// A previous plan (if any) seeds refinement: feedback is applied
	// against it instead of starting over.
	priorPlan := ""
	planFile := tasks.PlanFilePath(issueNum)
	{
		var stdoutBuf, stderrBuf bytes.Buffer
		if err := client.Exec(ctx, fmt.Sprintf("cat %s 2>/dev/null || true", planFile), "/workspaces", nil, nil, &stdoutBuf, &stderrBuf); err == nil {
			priorPlan = strings.TrimSpace(stdoutBuf.String())
		}
	}
	if flags.Feedback != "" && priorPlan == "" {
		fmt.Println("Note: --feedback given but no previous plan found in the sandbox; planning fresh with the feedback as guidance.")
	}

	promptBytes, err := tasks.RenderPlanPrompt(tasks.PlanParams{
		Issue:        *issue,
		Instructions: instructions,
		HTMLURL:      issue.GetHTMLURL(),
		PriorPlan:    priorPlan,
		Feedback:     flags.Feedback,
	})
	if err != nil {
		return fmt.Errorf("rendering plan prompt: %w", err)
	}

	scriptBytes, err := tasks.GetPlanScript()
	if err != nil {
		return fmt.Errorf("getting plan script: %w", err)
	}

	taskDir, reattached := client.ResolveTaskDir(ctx, "plan")
	promptPath := fmt.Sprintf("%s/agent-prompt.txt", taskDir)
	scriptPath := fmt.Sprintf("%s/pre-script.sh", taskDir)

	if reattached {
		fmt.Printf("Reattaching to in-flight task %s...\n", taskDir)
	} else {
		fmt.Println("Writing prompt and script into sandbox...")
		if err := client.WriteFile(ctx, promptPath, promptBytes); err != nil {
			return fmt.Errorf("writing prompt: %w", err)
		}
		if err := client.WriteFile(ctx, scriptPath, scriptBytes); err != nil {
			return fmt.Errorf("writing script: %w", err)
		}
	}

	envMap := map[string]string{
		"GITHUB_TOKEN":               string(secret.Data[constants.KeyGithubToken]),
		"GEMINI_API_KEY":             getGeminiAPIKey(secret),
		"GEMINI_CLI_TRUST_WORKSPACE": "true",
		"REPO_NAME":                  repo,
		"CLONE_URL":                  cloneURL,
		"PROMPT_FILE":                promptPath,
		"GITHUB_USER_ID":             githubLogin,
		"GITHUB_USER_EMAIL":          githubEmail,
		"GITHUB_USER_NAME":           githubLogin,
		"ISSUE_NUMBER":               strconv.Itoa(issueNum),
		"MODELS":                     tasks.GetAvailableModelsForKey(getGeminiAPIKey(secret)),
	}

	fmt.Println("Running plan task via envd...")
	cmdStr := fmt.Sprintf("bash -c 'set -o pipefail; bash %s'", scriptPath)
	_ = factorysandbox.UpdateSandboxTaskAnnotation(ctx, kubeClient, rootFlags.Namespace, sandboxName, "plan", "Running")
	if err := client.RunTaskResilient(ctx, cmdStr, envMap, taskDir, rootFlags.Detached, rootFlags.AbortOnCancel); err != nil {
		_ = factorysandbox.UpdateSandboxTaskAnnotation(ctx, kubeClient, rootFlags.Namespace, sandboxName, "plan", "Failed")
		return fmt.Errorf("running task: %w", err)
	}
	if rootFlags.Detached {
		return nil
	}

	// The task state must reflect the whole run including output reading:
	// an empty plan is a failure, not a completed-looking sandbox.
	if err := finishPlan(ctx, client, taskDir, owner, repo, issueNum, sandboxName); err != nil {
		_ = factorysandbox.UpdateSandboxTaskAnnotation(ctx, kubeClient, rootFlags.Namespace, sandboxName, "plan", "Failed")
		return err
	}
	_ = factorysandbox.UpdateSandboxTaskAnnotation(ctx, kubeClient, rootFlags.Namespace, sandboxName, "plan", "Completed")
	return nil
}

func finishPlan(ctx context.Context, client *envd.Client, taskDir, owner, repo string, issueNum int, sandboxName string) error {
	usagereport.HarvestTask(ctx, client, taskDir, usagereport.Meta{
		Repo:     owner + "/" + repo,
		TaskType: "plan",
		Sandbox:  sandboxName,
		Issues:   []int{issueNum},
	})

	fmt.Println("\nPlan execution completed. Reading output...")

	var stdoutBuf bytes.Buffer
	var stderrBuf bytes.Buffer
	catCmd := fmt.Sprintf("cat %s/plan-output.txt", taskDir)
	if err := client.Exec(ctx, catCmd, "/workspaces", nil, nil, &stdoutBuf, &stderrBuf); err != nil {
		return fmt.Errorf("reading plan output from sandbox: %w (stderr: %s)", err, stderrBuf.String())
	}

	planOutput := strings.TrimSpace(stdoutBuf.String())
	if planOutput == "" {
		return fmt.Errorf("plan output was empty")
	}

	fmt.Println("\n================== ISSUE PLAN ==================")
	fmt.Println(planOutput)
	fmt.Println("================================================")
	fmt.Printf("Plan saved in the sandbox at %s — run `factory fix --url ... --with-plan` to implement it.\n", tasks.PlanFilePath(issueNum))
	return nil
}
