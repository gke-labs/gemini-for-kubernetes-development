package commands

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/envd"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
	factorysandbox "github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/sandbox"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/tasks"
	githubv39 "github.com/google/go-github/v39/github"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type ReviewFlags struct {
	PRURL        string
	Publish      string
	Instructions []string
}

func NewReviewCommand(ctx context.Context) *cobra.Command {
	var flags ReviewFlags

	cmd := &cobra.Command{
		Use:   "review",
		Short: "Review a GitHub pull request in a sandbox pod",
		Example: `  # Review a PR with specific instructions
  factory pr review --pr-url https://github.com/owner/repo/pull/1 --instruction docs/guidelines.md --instruction /path/to/local/rules.txt

  # Review and publish automatically
  factory pr review --pr-url https://github.com/owner/repo/pull/1 --publish yes

  # Review and post as a draft (pending) review comment
  factory pr review --pr-url https://github.com/owner/repo/pull/1 --publish draft`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := ResolveRootFlags(cmd)
			if err != nil {
				return err
			}

			if flags.PRURL == "" {
				return fmt.Errorf("--pr-url is required")
			}
			flags.Publish = strings.ToLower(strings.TrimSpace(flags.Publish))

			if flags.Publish != "yes" && flags.Publish != "no" && flags.Publish != "ask" && flags.Publish != "draft" {
				return fmt.Errorf("invalid value for --publish: %s. Must be one of [no, yes, ask, draft]", flags.Publish)
			}

			sessionName := "factory-pr-unknown-review"
			u, err := url.Parse(flags.PRURL)
			if err == nil {
				parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
				if len(parts) >= 4 && parts[2] == "pull" {
					sessionName = fmt.Sprintf("factory-pr-%s-review", parts[3])
				}
			}

			if rootFlags.Background {
				ran, err := checkAndRunInBackground(sessionName)
				if err != nil {
					return err
				}
				if ran {
					return nil // Parent process exits
				}
			}

			ctx, cancel := context.WithTimeout(ctx, rootFlags.Timeout)
			defer cancel()
			return runReview(ctx, flags.PRURL, flags.Publish, flags.Instructions, rootFlags.EphemeralStorage, rootFlags.ResolvedSecrets)
		},
	}

	cmd.Flags().StringVar(&flags.PRURL, "pr-url", "", "GitHub PR URL (e.g. https://github.com/owner/repo/pull/123)")
	cmd.Flags().StringVar(&flags.Publish, "publish", "no", "Publish policy: yes (publish to github), no (print on screen only), ask (print on screen and ask y/n), draft (post as a draft pending comment on github)")
	cmd.Flags().StringSliceVar(&flags.Instructions, "instruction", []string{}, "Repeatable set of instruction files (local/repo) or raw instruction strings")

	return cmd
}

func readInstructionFile(ctx context.Context, ghClient *githubv39.Client, owner, repo, ref, path string) (string, error) {
	// 1. Try reading from local filesystem first
	data, err := os.ReadFile(path)
	if err == nil {
		return string(data), nil
	}

	// 2. If not found locally, try fetching from GitHub repo
	if ghClient != nil {
		fileContent, _, _, err := ghClient.Repositories.GetContents(ctx, owner, repo, path, &githubv39.RepositoryContentGetOptions{Ref: ref})
		if err == nil && fileContent != nil {
			content, err := fileContent.GetContent()
			if err == nil {
				return content, nil
			}
		}
	}

	return "", fmt.Errorf("instruction file not found locally or in repo: %s", path)
}

func resolveInstruction(ctx context.Context, ghClient *githubv39.Client, owner, repo, ref, val string) (string, bool, error) {
	content, err := readInstructionFile(ctx, ghClient, owner, repo, ref, val)
	if err == nil {
		return content, true, nil
	}

	if isLikelyFilePath(val) {
		return "", true, err
	}

	return val, false, nil
}

func isLikelyFilePath(val string) bool {
	val = strings.TrimSpace(val)
	if val == "" {
		return false
	}
	if strings.Contains(val, "\n") {
		return false
	}
	if strings.HasPrefix(val, "/") || strings.HasPrefix(val, "./") || strings.HasPrefix(val, "../") || strings.HasPrefix(val, "~/") {
		return true
	}
	if strings.Contains(val, "/") || strings.Contains(val, "\\") {
		return true
	}
	if strings.Contains(val, " ") {
		return false
	}
	return filepath.Ext(val) != ""
}

func stripUntilIndicator(input string, indicator string) string {
	if indicator == "" {
		return input
	}
	if strings.HasPrefix(input, indicator) {
		return input
	}
	search := "\n" + indicator
	if idx := strings.Index(input, search); idx != -1 {
		return input[idx+1:]
	}
	return input
}

func stripYAMLMarkers(input string) string {
	trimmed := strings.TrimSpace(input)

	// Strip the prefix if it exists
	if strings.HasPrefix(trimmed, "```yaml") {
		trimmed = strings.TrimPrefix(trimmed, "```yaml")
		trimmed = strings.TrimSpace(trimmed)
	}

	// Strip the suffix if it exists (regardless of whether prefix existed)
	if strings.HasSuffix(trimmed, "```") {
		trimmed = strings.TrimSuffix(trimmed, "```")
		trimmed = strings.TrimSpace(trimmed)
	}

	return trimmed
}

func runReview(ctx context.Context, prURL string, publishPolicy string, instructionPaths []string, ephemeralStorage string, secrets []factorysandbox.SecretMount) error {
	fmt.Printf("Resolving PR URL: %s...\n", prURL)

	u, err := url.Parse(prURL)
	if err != nil {
		return fmt.Errorf("invalid PR URL: %w", err)
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(parts) < 4 || parts[2] != "pull" {
		return fmt.Errorf("expected URL format https://github.com/owner/repo/pull/123, got %s", prURL)
	}
	owner, repo := parts[0], parts[1]
	prNum, err := strconv.Atoi(parts[3])
	if err != nil {
		return fmt.Errorf("invalid PR number in URL: %s", parts[3])
	}

	ghClient, err := github.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("creating github client: %w", err)
	}
	pr, _, err := ghClient.PullRequests.Get(ctx, owner, repo, prNum)
	if err != nil {
		return fmt.Errorf("fetching github PR #%d: %w", prNum, err)
	}

	ref := pr.GetHead().GetSHA()

	// Read and accumulate all instructions
	var instructions []string
	for _, instVal := range instructionPaths {
		content, isFile, err := resolveInstruction(ctx, ghClient, owner, repo, ref, instVal)
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

	cloneURL := pr.GetBase().GetRepo().GetCloneURL()
	fmt.Printf("Ensuring review sandbox for PR #%d...\n", prNum)
	sandboxName, err := factorysandbox.EnsureReviewSandbox(ctx, kubeClient, rootFlags.Namespace, prNum, pr.GetTitle(), pr.GetHTMLURL(), pr.GetDiffURL(), cloneURL, rootFlags.Image, rootFlags.DiskSize, ephemeralStorage, secrets, rootFlags.ResolvedEnvs)
	if err != nil {
		return fmt.Errorf("ensuring review sandbox: %w", err)
	}

	secret, err := kubeClient.Clientset.CoreV1().Secrets(rootFlags.Namespace).Get(ctx, rootFlags.SecretName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("fetching %s secret in namespace %s: %w (make sure to run 'factory user onboard' first)", rootFlags.SecretName, rootFlags.Namespace, err)
	}
	githubLogin := string(secret.Data[KeyGithubLogin])
	githubEmail := string(secret.Data[KeyGithubEmail])

	structuredParams := tasks.StructuredReviewParams{
		PullRequest:  *pr,
		Instructions: instructions,
		DiffURL:      pr.GetDiffURL(),
		HTMLURL:      pr.GetHTMLURL(),
	}
	promptBytes, err := tasks.RenderStructuredReviewPrompt(structuredParams)
	if err != nil {
		return fmt.Errorf("rendering structured review prompt: %w", err)
	}

	scriptBytes, err := tasks.GetReviewScript()
	if err != nil {
		return fmt.Errorf("getting review script: %w", err)
	}

	fmt.Printf("Connecting to sandbox %s via envd...\n", sandboxName)
	client, err := envd.Connect(ctx, rootFlags.Namespace, sandboxName)
	if err != nil {
		return fmt.Errorf("connecting to sandbox: %w", err)
	}
	defer client.Close()

	taskDir := fmt.Sprintf("/workspaces/tasks/review-%s", time.Now().Format("20060102-150405"))
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
		"GITHUB_TOKEN":               string(secret.Data[KeyGithubToken]),
		"GEMINI_API_KEY":             getGeminiAPIKey(secret),
		"GEMINI_CLI_TRUST_WORKSPACE": "true",
		"REPO_NAME":                  repo,
		"CLONE_URL":                  cloneURL,
		"PROMPT_FILE":                promptPath,
		"GITHUB_USER_ID":             githubLogin,
		"GITHUB_USER_EMAIL":          githubEmail,
		"GITHUB_USER_NAME":           githubLogin,
		"PR_NUMBER":                  strconv.Itoa(prNum),
		"MODELS":                     "gemini-3.5-flash gemini-3-flash-preview gemini-3.1-pro-preview gemini-2.5-pro",
	}

	fmt.Println("Running review task via envd...")
	cmdStr := fmt.Sprintf("bash -c 'set -o pipefail; bash %s'", scriptPath)
	_ = factorysandbox.UpdateSandboxTaskAnnotation(ctx, kubeClient, rootFlags.Namespace, sandboxName, "review", "Running")
	if err := client.RunTaskResilient(ctx, cmdStr, envMap, taskDir, rootFlags.Detached); err != nil {
		_ = factorysandbox.UpdateSandboxTaskAnnotation(ctx, kubeClient, rootFlags.Namespace, sandboxName, "review", "Failed")
		return fmt.Errorf("running task: %w", err)
	}
	if rootFlags.Detached {
		return nil
	}
	_ = factorysandbox.UpdateSandboxTaskAnnotation(ctx, kubeClient, rootFlags.Namespace, sandboxName, "review", "Completed")

	fmt.Println("\nReview execution completed. Reading output...")

	var stdoutBuf bytes.Buffer
	var stderrBuf bytes.Buffer
	catCmd := fmt.Sprintf("cat %s/review-output.txt", taskDir)
	if err := client.Exec(ctx, catCmd, "/workspaces", nil, nil, &stdoutBuf, &stderrBuf); err != nil {
		return fmt.Errorf("reading review output from sandbox: %w (stderr: %s)", err, stderrBuf.String())
	}

	reviewOutput := strings.TrimSpace(stdoutBuf.String())
	if reviewOutput == "" {
		return fmt.Errorf("review output was empty")
	}

	reviewOutput = stripYAMLMarkers(reviewOutput)
	reviewOutput = stripUntilIndicator(reviewOutput, "review:")

	var agentOutput tasks.ReviewAgentOutput
	if err := yaml.Unmarshal([]byte(reviewOutput), &agentOutput); err != nil {
		return fmt.Errorf("parsing structured review output: %w", err)
	}
	structuredOutput := &agentOutput

	shouldPublish := false
	isDraft := false
	switch publishPolicy {
	case "yes":
		shouldPublish = true
	case "draft":
		shouldPublish = true
		isDraft = true
	case "no":
		fmt.Println("\n================= CODE REVIEW =================")
		fmt.Println(reviewOutput)
		fmt.Println("===============================================")
	case "ask":
		fmt.Println("\n================= CODE REVIEW =================")
		fmt.Println(reviewOutput)
		fmt.Println("===============================================")

		fmt.Print("Do you want to post this review to the PR? (y/N/d for draft): ")
		var response string
		if _, err := fmt.Scanln(&response); err != nil {
			response = "n"
		}
		response = strings.ToLower(strings.TrimSpace(response))
		if response == "y" || response == "yes" {
			shouldPublish = true
		} else if response == "d" || response == "draft" {
			shouldPublish = true
			isDraft = true
		}
	}

	if shouldPublish {
		var reviewEvent *string
		if !isDraft {
			reviewEvent = githubv39.String("COMMENT")
			fmt.Println("Posting review to GitHub PR...")
		} else {
			fmt.Println("Posting review as a draft (pending) review to GitHub PR...")
		}

		var reviewRequest *githubv39.PullRequestReviewRequest
		if structuredOutput != nil && structuredOutput.Review != nil {
			var comments []*githubv39.DraftReviewComment
			for _, c := range structuredOutput.Review.Comments {
				comments = append(comments, &githubv39.DraftReviewComment{
					Path:      c.Path,
					Position:  c.Position,
					Body:      c.Body,
					Line:      c.Line,
					Side:      c.Side,
					StartLine: c.StartLine,
					StartSide: c.StartSide,
				})
			}
			reviewRequest = &githubv39.PullRequestReviewRequest{
				Body:     structuredOutput.Review.Body,
				Event:    reviewEvent,
				Comments: comments,
			}
		} else {
			reviewRequest = &githubv39.PullRequestReviewRequest{
				Body:  githubv39.String(reviewOutput),
				Event: reviewEvent,
			}
		}
		_, _, err = ghClient.PullRequests.CreateReview(ctx, owner, repo, prNum, reviewRequest)
		if err != nil {
			return fmt.Errorf("failed to create review on GitHub: %w", err)
		}
		if !isDraft {
			fmt.Println("Review successfully posted to the PR!")
		} else {
			fmt.Println("Draft review successfully created on the PR!")
		}
	} else {
		fmt.Println("Review was not posted.")
	}

	return nil
}
