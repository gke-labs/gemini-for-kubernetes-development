package commands

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/envd"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
	factorysandbox "github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/sandbox"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/tasks"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

type FixFlags struct {
	URL             string
	Instruction     string
	InstructionFile string
	Name            string
	NoPR            bool
	Watch           bool
	PollInterval    time.Duration
	WatchTimeout    time.Duration
}

func NewFixCommand(ctx context.Context) *cobra.Command {
	var flags FixFlags

	cmd := &cobra.Command{
		Use:   "fix",
		Short: "Create a pull request for a given GitHub issue or instructions in a sandbox",
		Example: `  # Fix an issue with a custom instruction
  factory fix --url https://github.com/owner/repo/issues/1 --instruction "Use Go 1.26 and add unit tests"

  # Execute a task on a repository without an issue (requires --name)
  factory fix --url https://github.com/owner/repo --name refactor-auth --instruction "Refactor the auth package"

  # Execute a task reading instruction from a file
  factory fix --url https://github.com/owner/repo --name refactor-auth --instruction-file ./prompt.txt

  # Override workspace disk size and base image
  factory fix --url https://github.com/owner/repo/issues/1 --workspace-disk-size 20Gi --image kind.local/my-golang:latest`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := ResolveRootFlags(cmd)
			if err != nil {
				return err
			}

			if flags.URL == "" {
				return fmt.Errorf("--url is required")
			}
			if flags.Instruction != "" && flags.InstructionFile != "" {
				return fmt.Errorf("cannot specify both --instruction and --instruction-file")
			}
			prompt := flags.Instruction
			if flags.InstructionFile != "" {
				content, err := os.ReadFile(flags.InstructionFile)
				if err != nil {
					return fmt.Errorf("reading instruction file: %w", err)
				}
				prompt = strings.TrimSpace(string(content))
			}
			if prompt == "" {
				prompt = "Fix this issue in the repository and push a PR"
			}

			sessionName := "factory-fix"
			u, err := url.Parse(flags.URL)
			if err == nil {
				parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
				if len(parts) >= 2 {
					repo := parts[1]
					taskID := "task"
					if len(parts) >= 4 && parts[2] == "issues" {
						taskID = parts[3]
					} else if flags.Name != "" {
						taskID = flags.Name
					}
					sessionName = fmt.Sprintf("fix-%s-%s-fix", repo, taskID)
				}
			}

			if rootFlags.Background {
				ran, err := checkAndRunInBackground(sessionName)
				if err != nil {
					return err
				}
				if ran {
					return nil // Parent exits
				}
			}

			timeout := rootFlags.Timeout
			if flags.Watch {
				if flags.WatchTimeout == 0 {
					timeout = 0
				} else {
					timeout = flags.WatchTimeout + 1*time.Hour
				}
			}

			if timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, timeout)
				defer cancel()
			}
			return runFix(ctx, flags.URL, prompt, flags.Name, flags.NoPR, flags.Watch, flags.PollInterval, flags.WatchTimeout, rootFlags.EphemeralStorage, rootFlags.ResolvedSecrets)
		},
	}

	cmd.Flags().StringVar(&flags.URL, "url", "", "GitHub issue or repository URL (e.g. https://github.com/owner/repo/issues/123 or https://github.com/owner/repo)")
	cmd.Flags().StringVar(&flags.Instruction, "instruction", "", "Custom instruction for the fix task")
	cmd.Flags().StringVar(&flags.InstructionFile, "instruction-file", "", "Path to a file containing custom instruction for the fix task")
	cmd.Flags().StringVar(&flags.Name, "name", "", "Short name for the sandbox (required when URL is a repository URL without an issue number)")
	cmd.Flags().BoolVar(&flags.NoPR, "no-pr", false, "Commit changes and push branch remotely, but do not create a pull request")
	cmd.Flags().BoolVar(&flags.Watch, "watch", false, "Watch the created pull request for check failures and new review comments")
	cmd.Flags().DurationVar(&flags.PollInterval, "poll-interval", 2*time.Minute, "Polling interval for watching the PR")
	cmd.Flags().DurationVar(&flags.WatchTimeout, "watch-timeout", 0, "Timeout for watching the PR (default forever)")

	return cmd
}

func runFix(ctx context.Context, targetURL, prompt, name string, noPR, watch bool, pollInterval time.Duration, watchTimeout time.Duration, ephemeralStorage string, secrets []factorysandbox.SecretMount) error {
	if targetURL == "" {
		return fmt.Errorf("--url is required to determine the repository")
	}
	fmt.Printf("Resolving target URL: %s...\n", targetURL)

	u, err := url.Parse(targetURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	path := strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), ".git")
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		return fmt.Errorf("expected URL format https://github.com/owner/repo or https://github.com/owner/repo/issues/123, got %s", targetURL)
	}
	owner, repo := parts[0], parts[1]

	var issueNum int
	var issueTitle string
	isIssue := false
	cloneURL := fmt.Sprintf("https://github.com/%s/%s.git", owner, repo)

	if len(parts) >= 4 && parts[2] == "issues" {
		isIssue = true
		issueNum, err = strconv.Atoi(parts[3])
		if err != nil {
			return fmt.Errorf("invalid issue number in URL: %s", parts[3])
		}
		issueTitle = fmt.Sprintf("Issue #%d", issueNum)
	} else {
		if name == "" {
			return fmt.Errorf("--name is required when URL is a repository URL without an issue number")
		}
		issueTitle = fmt.Sprintf("Task: %s", name)
	}

	kubeClient, err := clients.NewKubernetesClient()
	if err != nil {
		return fmt.Errorf("creating k8s client: %w", err)
	}

	var sandboxName string
	if isIssue {
		fmt.Printf("Ensuring sandbox for issue #%d...\n", issueNum)
		sandboxName, err = factorysandbox.EnsureFixSandbox(ctx, kubeClient, rootFlags.Namespace, repo, strconv.Itoa(issueNum), cloneURL, issueTitle, rootFlags.Image, rootFlags.DiskSize, ephemeralStorage, secrets, rootFlags.ResolvedEnvs)
	} else {
		fmt.Printf("Ensuring sandbox for task %s on repo %s/%s...\n", name, owner, repo)
		sandboxName, err = factorysandbox.EnsureFixSandbox(ctx, kubeClient, rootFlags.Namespace, repo, name, cloneURL, issueTitle, rootFlags.Image, rootFlags.DiskSize, ephemeralStorage, secrets, rootFlags.ResolvedEnvs)
	}
	if err != nil {
		return fmt.Errorf("ensuring sandbox: %w", err)
	}

	secret, err := kubeClient.Clientset.CoreV1().Secrets(rootFlags.Namespace).Get(ctx, rootFlags.SecretName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("fetching %s secret in namespace %s: %w (make sure to run 'factory user onboard' first)", rootFlags.SecretName, rootFlags.Namespace, err)
	}
	githubLogin := string(secret.Data[KeyGithubLogin])
	githubEmail := string(secret.Data[KeyGithubEmail])

	var branchName string
	var issueBody string
	var issueComments []tasks.IssueComment
	if isIssue {
		branchName = fmt.Sprintf("issue-%d-%d", issueNum, time.Now().Unix())
		fmt.Printf("Fetching details for issue #%d...\n", issueNum)
		ghClient, err := github.NewClient(ctx)
		if err != nil {
			return fmt.Errorf("creating github client: %w", err)
		}
		issue, _, err := ghClient.Issues.Get(ctx, owner, repo, issueNum)
		if err != nil {
			return fmt.Errorf("fetching github issue #%d: %w", issueNum, err)
		}
		issueBody = issue.GetBody()
		if issue.GetTitle() != "" {
			issueTitle = issue.GetTitle()
		}

		comments, _, err := ghClient.Issues.ListComments(ctx, owner, repo, issueNum, nil)
		if err == nil {
			for _, c := range comments {
				issueComments = append(issueComments, tasks.IssueComment{
					UserLogin: c.GetUser().GetLogin(),
					Body:      c.GetBody(),
				})
			}
		}
	} else {
		branchName = fmt.Sprintf("fix-%s-%d", name, time.Now().Unix())
		issueBody = prompt
	}

	params := tasks.FixIssueParams{
		Repo: tasks.Repo{
			CloneURL: cloneURL,
		},
		Issue: tasks.Issue{
			Number:  issueNum,
			HTMLURL: targetURL,
			Title:   issueTitle,
			Body:    issueBody,
		},
		IssueComments: issueComments,
		Instruction:   prompt,
		Branch:        branchName,
		Models:        []string{"gemini-3.5-flash", "gemini-3-flash-preview", "gemini-3.1-pro-preview", "gemini-2.5-pro"},
		DraftPR:       false,
		PRLabel:       "factory",
		NoPR:          noPR,
	}

	scriptBytes, err := tasks.GetFixIssueScript()
	if err != nil {
		return fmt.Errorf("getting fix-issue script: %w", err)
	}

	promptBytes, err := tasks.RenderFixIssuePrompt(params)
	if err != nil {
		return fmt.Errorf("rendering fix-issue prompt: %w", err)
	}

	fmt.Printf("Connecting to sandbox %s via envd...\n", sandboxName)
	client, err := envd.Connect(ctx, rootFlags.Namespace, sandboxName)
	if err != nil {
		return fmt.Errorf("connecting to sandbox: %w", err)
	}
	defer client.Close()

	taskDir := fmt.Sprintf("/workspaces/tasks/fix-%s", time.Now().Format("20060102-150405"))
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
		"REPO_OWNER":                 owner,
		"REPO_NAME":                  repo,
		"CLONE_URL":                  cloneURL,
		"ISSUE_NUMBER":               strconv.Itoa(issueNum),
		"PROMPT_FILE":                promptPath,
		"GITHUB_USER_ID":             githubLogin,
		"GITHUB_USER_EMAIL":          githubEmail,
		"GITHUB_USER_NAME":           githubLogin,
		"BRANCH_NAME":                branchName,
		"MODELS":                     "gemini-3.5-flash gemini-3-flash-preview gemini-3.1-pro-preview gemini-2.5-pro",
		"NO_PR":                      strconv.FormatBool(noPR),
	}

	fmt.Println("Running fix-issue task via envd...")
	cmdStr := fmt.Sprintf("bash -c 'set -o pipefail; bash %s'", scriptPath)
	_ = factorysandbox.UpdateSandboxTaskAnnotation(ctx, kubeClient, rootFlags.Namespace, sandboxName, "fix-issue", "Running")
	if err := client.RunTaskResilient(ctx, cmdStr, envMap, taskDir, rootFlags.Detached); err != nil {
		_ = factorysandbox.UpdateSandboxTaskAnnotation(ctx, kubeClient, rootFlags.Namespace, sandboxName, "fix-issue", "Failed")
		return fmt.Errorf("running task: %w", err)
	}
	if rootFlags.Detached {
		return nil
	}
	_ = factorysandbox.UpdateSandboxTaskAnnotation(ctx, kubeClient, rootFlags.Namespace, sandboxName, "fix-issue", "Completed")

	fmt.Println("\nTask execution completed.")

	var buf bytes.Buffer
	if err := client.Exec(ctx, fmt.Sprintf("cat %s/agent-output.txt", taskDir), "/workspaces", nil, nil, &buf, os.Stderr); err != nil {
		klog.Warningf("Could not read agent-output.txt: %v", err)
	}

	var prURL string
	var prNum int
	for _, line := range strings.Split(buf.String(), "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "/pull/") {
			prURL = line
			parts := strings.Split(line, "/")
			if len(parts) > 0 {
				if n, err := strconv.Atoi(parts[len(parts)-1]); err == nil {
					prNum = n
					break
				}
			}
		}
	}

	if prNum > 0 {
		fmt.Printf("Aliasing sandbox %s to PR #%d...\n", sandboxName, prNum)
		if err := factorysandbox.AliasSandboxToPR(ctx, kubeClient, rootFlags.Namespace, sandboxName, prNum); err != nil {
			klog.Warningf("Failed to alias sandbox to PR #%d: %v", prNum, err)
		}

		if watch {
			fmt.Printf("\nStarting PR watch for %s...\n", prURL)
			return runPRWatch(ctx, prURL, pollInterval, false, true, watchTimeout, ephemeralStorage, secrets)
		}
	} else if watch {
		fmt.Println("\nWarning: --watch was specified but could not determine PR URL from task output.")
	}

	if rootFlags.Cleanup {
		fmt.Printf("Cleaning up sandbox '%s'...\n", sandboxName)
		manager := k8s.NewManager(kubeClient)
		if err := manager.DeleteSandbox(ctx, rootFlags.Namespace, sandboxName); err != nil {
			klog.Errorf("Failed to cleanup sandbox '%s': %v", sandboxName, err)
		}
	}

	return nil
}
