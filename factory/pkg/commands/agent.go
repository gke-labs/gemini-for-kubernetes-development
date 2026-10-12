package commands

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/common"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/config"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/recipe"
	factorysandbox "github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/sandbox"
	githubv39 "github.com/google/go-github/v39/github"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

type AgentFlags struct {
	URL       string
	Agent     string
	Local     bool
	SessionID string
	RunName   string
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
		Long: `Run a custom agent definition (.agents/<name>.md) in the agent's own
sandbox, as the agent recipe (recipe/recipes/agent.yaml). A chore's
commits are pushed to the member's fork and opened as a PR (open-pr); on
a PR they are pushed to it; a skipPR agent commits nothing; a workflow's
journal is pushed to its factory-<issue> branch.`,
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
					agentName := common.Slugify(flags.Agent)
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
			return RunAgent(ctx, flags)
		},
	}

	cmd.Flags().StringVar(&flags.URL, "url", "", "GitHub repository or PR URL (e.g. https://github.com/owner/repo or https://github.com/owner/repo/pull/123)")
	cmd.Flags().StringVar(&flags.Agent, "agent", "", "Agent file name (relative to .agents/ if remote) or local file path")
	cmd.Flags().BoolVar(&flags.Local, "local", false, "Load the agent definition from a local path")
	cmd.Flags().StringVar(&flags.SessionID, "session-id", "", "Unique session ID for workflow isolation")
	cmd.Flags().StringVar(&flags.RunName, "run-name", "", "Name of this run: running again under it follows that run, or applies its result, instead of starting another")

	return cmd
}

func RunAgent(ctx context.Context, flags AgentFlags) error {
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
	repoClient := github.ForRepo(ghClient, owner, repo)

	// Get agent definition
	var content []byte
	flags.Agent = common.SanitizeWorkflowPath(flags.Agent)
	agentPath := flags.Agent
	if strings.HasPrefix(agentPath, "http://") || strings.HasPrefix(agentPath, "https://") {
		fmt.Printf("Fetching agent definition from URL: %s...\n", agentPath)
		content, err = common.FetchWorkflowContent(ctx, repoClient, agentPath)
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

	content, err = common.ResolveWorkflowRedirect(ctx, repoClient, content)
	if err != nil {
		return fmt.Errorf("resolving agent redirect: %w", err)
	}

	agentDef, err := common.ParseAgent(content)
	if err != nil {
		return fmt.Errorf("parsing agent definition: %w", err)
	}

	cfg, cfgErr := config.LoadConfig()
	if cfgErr != nil {
		klog.Warningf("Failed to load factory config: %v", cfgErr)
	}
	prompt := agentDef.Prompt
	if isPR || isIssue {
		fmt.Printf("Fetching details for #%d from GitHub...\n", targetNum)
		issue, _, err := ghClient.Issues.Get(ctx, owner, repo, targetNum)
		if err != nil {
			return fmt.Errorf("fetching issue/PR details: %w", err)
		}

		fmt.Printf("Fetching comments for #%d from GitHub...\n", targetNum)
		comments, err := repoClient.ListIssueComments(ctx, targetNum)
		if err != nil {
			return fmt.Errorf("fetching comments: %w", err)
		}
		// Only trusted authors' comments reach the prompt. On
		// a PR its author is trusted too: their description is already here.
		var prAuthor string
		if isPR {
			prAuthor = issue.GetUser().GetLogin()
		}
		var commentMsgs []string
		for _, c := range trustedIssueComments(comments, trustedLogins(cfg, prAuthor)) {
			commentMsgs = append(commentMsgs, fmt.Sprintf("Comment from %s:\n%s", c.GetUser().GetLogin(), c.GetBody()))
		}
		prompt = fmt.Sprintf("%s\n\nOriginal GitHub Context:\nTitle: %s\n\nDescription:\n%s\n\nComments:\n%s",
			agentDef.Prompt,
			issue.GetTitle(),
			issue.GetBody(),
			strings.Join(commentMsgs, "\n\n"),
		)
	}

	taskID := common.Slugify(agentDef.Name)
	if flags.SessionID != "" {
		taskID = fmt.Sprintf("%s-%s", taskID, flags.SessionID)
	} else if isPR {
		taskID = fmt.Sprintf("pr-%d-%s", targetNum, taskID)
	} else if isIssue {
		taskID = fmt.Sprintf("issue-%d-%s", targetNum, taskID)
	}
	taskTitle := fmt.Sprintf("Agent: %s", agentDef.Name)
	ensure := func(ctx context.Context, kubeClient *clients.KubernetesClient, cloneURL, htmlURL, user string) (string, error) {
		fmt.Printf("Ensuring sandbox for task %s...\n", taskID)
		return factorysandbox.EnsureAgentSandbox(ctx, kubeClient, rootFlags.Namespace, repo, taskID, cloneURL, flags.URL, taskTitle, rootFlags.Image, rootFlags.DiskSize, rootFlags.StorageClass, rootFlags.EphemeralStorage, rootFlags.ResolvedSecrets, rootFlags.ResolvedEnvs, user)
	}

	inputs := map[string]string{
		"prompt":       prompt,
		"agent_name":   agentDef.Name,
		"agent_file":   agentPath,
		"mode":         agentMode(agentDef, isPR),
		"session_id":   flags.SessionID,
		"precondition": agentDef.PreconditionScript,
		"labels":       resolvePRLabels(cfg, nil, false),
	}
	return runRecipeIn(ctx, ensure, recipe.AgentRecipe, flags.URL, flags.RunName, "", "", false,
		applyMode{on: true, verb: "open-pr", asUser: rootFlags.User != ""}, inputs, nil)
}

// agentMode is the agent recipe's mode for a definition run on a PR or
// not: a skipPR agent's changes are not kept, a PR's are pushed to it, a
// workflow's journal to its branch, and a chore's opened as a PR.
func agentMode(def *common.AgentDefinition, isPR bool) string {
	switch {
	case def.SkipPR:
		return "none"
	case isPR:
		return "pr"
	case def.Mode == "workflow":
		return "workflow"
	}
	return "chore"
}
