package commands

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
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
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/usagereport"
	githubv39 "github.com/google/go-github/v39/github"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

type AgentFlags struct {
	URL       string
	Agent     string
	Local     bool
	DryRun    bool
	SessionID string
}

type AgentDefinition struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Schedule    string `yaml:"schedule"`
	SkipPR      bool   `yaml:"skipPR,omitempty"`
	Mode        string `yaml:"mode,omitempty"`
	Cooldown    string `yaml:"cooldown,omitempty"`
	Prompt      string `yaml:"-"`
}

func NewAgentCommand(ctx context.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Manage and run custom agents in sandboxes",
	}
	cmd.AddCommand(NewAgentCreateCommand(ctx))
	return cmd
}

func NewAgentCreateCommand(ctx context.Context) *cobra.Command {
	var flags AgentFlags

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Run a custom agent definition in a sandbox",
		Example: `  # Run an agent defined in .agents/my-agent.yaml on a repository
  factory agent create --url https://github.com/owner/repo --agent my-agent.yaml

  # Run an agent defined locally in a sandbox for a PR
  factory agent create --url https://github.com/owner/repo/pull/123 --agent ./my-agent.yaml --local`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := ResolveRootFlags(cmd)
			if err != nil {
				return err
			}

			if flags.URL == "" {
				return fmt.Errorf("--url is required")
			}
			if flags.Agent == "" {
				return fmt.Errorf("--agent is required")
			}

			sessionName := "factory-agent"
			u, err := url.Parse(flags.URL)
			if err == nil {
				path := strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), ".git")
				parts := strings.Split(path, "/")
				if len(parts) >= 2 {
					repo := parts[1]
					agentName := Slugify(flags.Agent)
					taskID := agentName
					if len(parts) >= 4 && parts[2] == "pull" {
						taskID = fmt.Sprintf("pr-%s-%s", parts[3], agentName)
					}
					sessionName = fmt.Sprintf("agent-%s-%s-agent", repo, taskID)
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

			ctx, cancel := context.WithTimeout(ctx, rootFlags.Timeout)
			defer cancel()
			return RunAgent(ctx, flags, rootFlags.EphemeralStorage, rootFlags.ResolvedSecrets)
		},
	}

	cmd.Flags().StringVar(&flags.URL, "url", "", "GitHub repository or PR URL (e.g. https://github.com/owner/repo or https://github.com/owner/repo/pull/123)")
	cmd.Flags().StringVar(&flags.Agent, "agent", "", "Agent file name (relative to .agents/ if remote) or local file path")
	cmd.Flags().BoolVar(&flags.Local, "local", false, "Load the agent definition from a local path")
	cmd.Flags().BoolVar(&flags.DryRun, "dry-run", false, "Simulate agent execution without running the Gemini CLI inside the sandbox")
	cmd.Flags().StringVar(&flags.SessionID, "session-id", "", "Unique session ID for workflow isolation")

	return cmd
}

func RunAgent(ctx context.Context, flags AgentFlags, ephemeralStorage string, secrets []factorysandbox.SecretMount) error {
	fmt.Printf("Resolving target URL: %s...\n", flags.URL)

	u, err := url.Parse(flags.URL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	path := strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), ".git")
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		return fmt.Errorf("expected URL format https://github.com/owner/repo or https://github.com/owner/repo/pull/123, got %s", flags.URL)
	}
	owner, repo := parts[0], parts[1]

	var prNum int
	isPR := false
	isIssue := false
	targetNum := 0
	cloneURL := fmt.Sprintf("https://github.com/%s/%s.git", owner, repo)

	if len(parts) >= 4 {
		if parts[2] == "pull" {
			isPR = true
			targetNum, err = strconv.Atoi(parts[3])
			if err != nil {
				return fmt.Errorf("invalid PR number in URL: %s", parts[3])
			}
			prNum = targetNum
		} else if parts[2] == "issues" {
			isIssue = true
			targetNum, err = strconv.Atoi(parts[3])
			if err != nil {
				return fmt.Errorf("invalid issue number in URL: %s", parts[3])
			}
		}
	}

	ghClient, err := github.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("creating github client: %w", err)
	}

	// Get agent definition
	var content []byte
	agentPath := flags.Agent
	if strings.HasPrefix(agentPath, "http://") || strings.HasPrefix(agentPath, "https://") {
		fmt.Printf("Fetching agent definition from URL: %s...\n", agentPath)
		content, err = fetchWorkflowContent(ctx, ghClient, agentPath)
		if err != nil {
			return fmt.Errorf("fetching agent from URL %s: %w", agentPath, err)
		}
	} else if flags.Local {
		fmt.Printf("Loading local agent definition: %s...\n", agentPath)
		content, err = os.ReadFile(agentPath)
		if err != nil {
			return fmt.Errorf("reading local agent file: %w", err)
		}
	} else {
		if !strings.Contains(agentPath, "/") && !strings.HasPrefix(agentPath, ".") {
			agentPath = ".agents/" + agentPath
		}

		var ref string
		if isPR {
			fmt.Printf("Fetching PR #%d details from GitHub...\n", prNum)
			pr, _, err := ghClient.PullRequests.Get(ctx, owner, repo, prNum)
			if err != nil {
				return fmt.Errorf("fetching PR from github: %w", err)
			}
			ref = pr.GetHead().GetSHA()
		}

		fmt.Printf("Fetching remote agent definition %s (ref: %s) from repository...\n", agentPath, ref)
		fileContent, _, _, err := ghClient.Repositories.GetContents(ctx, owner, repo, agentPath, &githubv39.RepositoryContentGetOptions{Ref: ref})
		if err != nil {
			return fmt.Errorf("getting agent definition from github: %w", err)
		}
		contentStr, err := fileContent.GetContent()
		if err != nil {
			return fmt.Errorf("decoding agent definition content: %w", err)
		}
		content = []byte(contentStr)
	}

	agentDef, err := ParseAgent(content)
	if err != nil {
		return fmt.Errorf("parsing agent definition: %w", err)
	}

	kubeClient, err := clients.NewKubernetesClient()
	if err != nil {
		return fmt.Errorf("creating k8s client: %w", err)
	}

	taskID := Slugify(agentDef.Name)
	if flags.SessionID != "" {
		taskID = fmt.Sprintf("%s-%s", taskID, flags.SessionID)
	} else if isPR {
		taskID = fmt.Sprintf("pr-%d-%s", targetNum, taskID)
	} else if isIssue {
		taskID = fmt.Sprintf("issue-%d-%s", targetNum, taskID)
	}
	taskTitle := fmt.Sprintf("Agent: %s", agentDef.Name)

	fmt.Printf("Ensuring sandbox for task %s...\n", taskID)
	sandboxName, err := factorysandbox.EnsureAgentSandbox(ctx, kubeClient, rootFlags.Namespace, repo, taskID, cloneURL, flags.URL, taskTitle, rootFlags.Image, rootFlags.DiskSize, ephemeralStorage, secrets, rootFlags.ResolvedEnvs, rootFlags.User)
	if err != nil {
		return fmt.Errorf("ensuring agent sandbox: %w", err)
	}

	secret, err := kubeClient.Clientset.CoreV1().Secrets(rootFlags.Namespace).Get(ctx, rootFlags.SecretName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("fetching %s secret in namespace %s: %w (make sure to run 'factory user onboard' first)", rootFlags.SecretName, rootFlags.Namespace, err)
	}
	githubLogin := string(secret.Data[KeyGithubLogin])
	githubEmail := string(secret.Data[KeyGithubEmail])

	fmt.Printf("Connecting to sandbox %s via envd...\n", sandboxName)
	client, err := envd.Connect(ctx, rootFlags.Namespace, sandboxName)
	if err != nil {
		return fmt.Errorf("connecting to sandbox: %w", err)
	}
	defer client.Close()

	var prompt string
	if isPR || isIssue {
		fmt.Printf("Fetching details for #%d from GitHub...\n", targetNum)
		issue, _, err := ghClient.Issues.Get(ctx, owner, repo, targetNum)
		if err != nil {
			return fmt.Errorf("fetching issue/PR details: %w", err)
		}

		fmt.Printf("Fetching comments for #%d from GitHub...\n", targetNum)
		comments, err := github.ListAllIssueComments(ctx, ghClient, owner, repo, targetNum)
		if err != nil {
			return fmt.Errorf("fetching comments: %w", err)
		}
		var commentMsgs []string
		for _, c := range comments {
			commentMsgs = append(commentMsgs, fmt.Sprintf("Comment from %s:\n%s", c.GetUser().GetLogin(), c.GetBody()))
		}

		prompt = fmt.Sprintf("%s\n\nOriginal GitHub Context:\nTitle: %s\n\nDescription:\n%s\n\nComments:\n%s",
			agentDef.Prompt,
			issue.GetTitle(),
			issue.GetBody(),
			strings.Join(commentMsgs, "\n\n"),
		)
	} else {
		prompt = agentDef.Prompt
	}

	taskDir := fmt.Sprintf("/workspaces/tasks/agent-%s-%s", Slugify(agentDef.Name), time.Now().Format("20060102-150405"))
	promptPath := fmt.Sprintf("%s/agent-prompt.txt", taskDir)
	scriptPath := fmt.Sprintf("%s/pre-script.sh", taskDir)

	params := tasks.AgentParams{
		AgentPrompt: prompt,
		AgentName:   agentDef.Name,
		AgentFile:   agentPath,
		RepoName:    repo,
		CloneURL:    cloneURL,
		RepoOwner:   owner,
		PromptFile:  promptPath,
		SkipPR:      agentDef.SkipPR,
		PRNumber:    prNum,
		Models:      []string{"gemini-3.5-flash", "gemini-3-flash-preview", "gemini-3.1-pro-preview", "gemini-2.5-pro"},
	}

	promptBytes, err := tasks.RenderRunAgentPrompt(params)
	if err != nil {
		return fmt.Errorf("rendering agent prompt: %w", err)
	}

	scriptBytes, err := tasks.GetRunAgentScript()
	if err != nil {
		return fmt.Errorf("getting run agent script: %w", err)
	}

	fmt.Println("Writing prompt and script into sandbox...")
	if err := client.WriteFile(ctx, promptPath, promptBytes); err != nil {
		return fmt.Errorf("writing prompt: %w", err)
	}
	if err := client.WriteFile(ctx, scriptPath, scriptBytes); err != nil {
		return fmt.Errorf("writing script: %w", err)
	}

	envMap := map[string]string{
		"HOME":                       "/workspaces/.home",
		"FACTORY_CONFIG":             "/workspaces/.factory.cfg",
		"GITHUB_TOKEN":               string(secret.Data[KeyGithubToken]),
		"GEMINI_API_KEY":             getGeminiAPIKey(secret),
		"GEMINI_CLI_TRUST_WORKSPACE": "true",
		"REPO_OWNER":                 owner,
		"REPO_NAME":                  repo,
		"CLONE_URL":                  cloneURL,
		"PROMPT_FILE":                promptPath,
		"GITHUB_USER_ID":             githubLogin,
		"GITHUB_USER_EMAIL":          githubEmail,
		"GITHUB_USER_NAME":           githubLogin,
		"AGENT_NAME":                 agentDef.Name,
		"AGENT_FILE":                 agentPath,
		"AGENT_MODE":                 agentDef.Mode,
		"SESSION_ID":                 flags.SessionID,
		"SKIP_PR":                    strconv.FormatBool(agentDef.SkipPR),
		"PR_NUMBER":                  strconv.Itoa(prNum),
		"MODELS":                     "gemini-3.5-flash gemini-3-flash-preview gemini-3.1-pro-preview gemini-2.5-pro",
		"DRY_RUN":                    strconv.FormatBool(flags.DryRun),
	}

	fmt.Println("Running agent task via envd...")
	cmdStr := fmt.Sprintf("bash -c 'set -o pipefail; bash %s'", scriptPath)
	_ = factorysandbox.UpdateSandboxTaskAnnotation(ctx, kubeClient, rootFlags.Namespace, sandboxName, "agent", "Running")
	if err := client.RunTaskResilient(ctx, cmdStr, envMap, taskDir, rootFlags.Detached, rootFlags.AbortOnCancel); err != nil {
		_ = factorysandbox.UpdateSandboxTaskAnnotation(ctx, kubeClient, rootFlags.Namespace, sandboxName, "agent", "Failed")
		return fmt.Errorf("running task: %w", err)
	}
	if rootFlags.Detached {
		return nil
	}
	_ = factorysandbox.UpdateSandboxTaskAnnotation(ctx, kubeClient, rootFlags.Namespace, sandboxName, "agent", "Completed")

	fmt.Println("\nAgent execution completed.")

	var buf bytes.Buffer
	if err := client.Exec(ctx, fmt.Sprintf("cat %s/agent-output.txt", taskDir), "/workspaces", nil, nil, &buf, os.Stderr); err != nil {
		klog.Warningf("Could not read agent-output.txt: %v", err)
	} else {
		fmt.Printf("\nAgent Output:\n%s\n", buf.String())
	}

	var prURL string
	var createdPRNum int
	for _, line := range strings.Split(buf.String(), "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "/pull/") {
			prURL = line
			parts := strings.Split(line, "/")
			if len(parts) > 0 {
				if n, err := strconv.Atoi(parts[len(parts)-1]); err == nil {
					createdPRNum = n
					break
				}
			}
		}
	}

	if createdPRNum > 0 {
		fmt.Printf("Aliasing sandbox %s to PR #%d...\n", sandboxName, createdPRNum)
		if err := factorysandbox.AliasSandboxToPR(ctx, kubeClient, rootFlags.Namespace, sandboxName, createdPRNum, prURL); err != nil {
			klog.Warningf("Failed to alias sandbox to PR #%d: %v", createdPRNum, err)
		}
		fmt.Printf("PR created/updated: %s\n", prURL)
	}

	usageMeta := usagereport.Meta{
		Repo:         owner + "/" + repo,
		TaskType:     "agent",
		Sandbox:      sandboxName,
		PR:           createdPRNum,
		Workflow:     flags.SessionID,
		WorkflowName: agentDef.Name,
	}
	if isIssue {
		usageMeta.Issue = targetNum
	} else if isPR && usageMeta.PR == 0 {
		usageMeta.PR = targetNum
	}
	if numStr, ok := strings.CutPrefix(flags.SessionID, "issue-"); ok && usageMeta.Issue == 0 {
		if n, err := strconv.Atoi(numStr); err == nil {
			usageMeta.Issue = n
		}
	}
	usagereport.HarvestTask(ctx, client, taskDir, usageMeta)
	if usagereport.Enabled() {
		if usageMeta.Issue != 0 {
			if issue, _, err := ghClient.Issues.Get(ctx, owner, repo, usageMeta.Issue); err == nil {
				usagereport.ReportIssueSubject(ctx, owner+"/"+repo, issue)
			}
		}
		if usageMeta.PR != 0 {
			if subjectPR, _, err := ghClient.PullRequests.Get(ctx, owner, repo, usageMeta.PR); err == nil {
				usagereport.ReportPRSubject(ctx, owner+"/"+repo, subjectPR)
			}
		}
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

func ParseAgent(content []byte) (*AgentDefinition, error) {
	parts := strings.SplitN(string(content), "---", 3)
	if len(parts) < 3 {
		return nil, fmt.Errorf("invalid agent definition format: missing frontmatter")
	}

	var def AgentDefinition
	if err := yaml.Unmarshal([]byte(parts[1]), &def); err != nil {
		return nil, fmt.Errorf("failed to unmarshal frontmatter: %w", err)
	}

	def.Prompt = strings.TrimSpace(parts[2])
	return &def, nil
}

func Slugify(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, " ", "-")
	var res strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			res.WriteRune(r)
		}
	}
	return res.String()
}

func parseGitHubURL(urlStr string) (owner, repo, branch, path string, ok bool) {
	u, err := url.Parse(urlStr)
	if err != nil {
		return "", "", "", "", false
	}
	if u.Host != "github.com" {
		return "", "", "", "", false
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(parts) < 4 || (parts[2] != "blob" && parts[2] != "raw") {
		return "", "", "", "", false
	}
	owner = parts[0]
	repo = parts[1]
	branch = parts[3]
	path = strings.Join(parts[4:], "/")
	return owner, repo, branch, path, true
}

func fetchWorkflowContent(ctx context.Context, ghClient *githubv39.Client, urlStr string) ([]byte, error) {
	if owner, repo, branch, path, ok := parseGitHubURL(urlStr); ok {
		klog.Infof("Fetching agent from GitHub repository %s/%s at branch/ref %s, path %s", owner, repo, branch, path)
		fileContent, _, _, err := ghClient.Repositories.GetContents(ctx, owner, repo, path, &githubv39.RepositoryContentGetOptions{Ref: branch})
		if err != nil {
			return nil, fmt.Errorf("fetching content from GitHub repo: %w", err)
		}
		if fileContent == nil {
			return nil, fmt.Errorf("content is nil (possibly a directory or submodule)")
		}
		contentStr, err := fileContent.GetContent()
		if err != nil {
			return nil, fmt.Errorf("decoding GitHub content: %w", err)
		}
		return []byte(contentStr), nil
	}

	klog.Infof("Fetching agent from HTTP URL %s", urlStr)
	req, err := http.NewRequestWithContext(ctx, "GET", urlStr, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP GET request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP GET returned status %d", resp.StatusCode)
	}

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
