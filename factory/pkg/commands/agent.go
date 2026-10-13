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
sandbox. A chore runs on the repository as the agent recipe
(recipe/recipes/agent.yaml): its commits are pushed to the member's fork
and opened as a PR (open-pr), and a skipPR chore commits nothing. A
workflow (mode: workflow) takes its next step on its issue as the workflow
recipe (recipe/recipes/workflow.yaml), whose journal is pushed to the
branch factory-<issue>. To change a PR, use factory recipe care-iterate.`,
		Example: `  # Run an agent defined in .agents/my-agent.yaml on a repository
  factory agent create --url https://github.com/owner/repo --agent my-agent.yaml

  # Run a workflow defined locally for an issue
  factory agent create --url https://github.com/owner/repo/issues/123 --agent ./my-workflow.md --local --session-id issue-123`,
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

	cmd.Flags().StringVar(&flags.URL, "url", "", "GitHub repository or issue URL (e.g. https://github.com/owner/repo or https://github.com/owner/repo/issues/123)")
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
		return fmt.Errorf("expected URL format https://github.com/owner/repo or https://github.com/owner/repo/issues/123, got %s", flags.URL)
	}
	owner, repo := parts[0], parts[1]

	isIssue := false
	targetNum := 0

	if len(parts) >= 4 {
		if parts[2] == "pull" {
			return fmt.Errorf("an agent runs on a repository or an issue, not a PR; to change a PR as an instruction asks, run factory recipe care-iterate --url %s --instruction …", flags.URL)
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

		fmt.Printf("Fetching remote agent definition %s from repository...\n", agentPath)
		fileContent, _, _, err := ghClient.Repositories.GetContents(ctx, owner, repo, agentPath, nil)
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
	workflow := isWorkflow(agentDef)
	switch {
	case workflow && !isIssue:
		return fmt.Errorf("%s is a workflow, which runs on an issue: --url https://github.com/%s/%s/issues/<n>", agentDef.Name, owner, repo)
	case !workflow && isIssue:
		return fmt.Errorf("%s is a chore, which runs on the repository: --url https://github.com/%s/%s", agentDef.Name, owner, repo)
	}
	if workflow && flags.SessionID == "" {
		flags.SessionID = fmt.Sprintf("issue-%d", targetNum)
	}

	taskID := common.Slugify(agentDef.Name)
	if flags.SessionID != "" {
		taskID = fmt.Sprintf("%s-%s", taskID, flags.SessionID)
	}
	taskTitle := fmt.Sprintf("Agent: %s", agentDef.Name)
	ensure := func(ctx context.Context, kubeClient *clients.KubernetesClient, cloneURL, htmlURL, user string) (string, error) {
		fmt.Printf("Ensuring sandbox for task %s...\n", taskID)
		return factorysandbox.EnsureAgentSandbox(ctx, kubeClient, rootFlags.Namespace, repo, taskID, cloneURL, flags.URL, taskTitle, rootFlags.Image, rootFlags.DiskSize, rootFlags.StorageClass, rootFlags.EphemeralStorage, rootFlags.ResolvedSecrets, rootFlags.ResolvedEnvs, user)
	}

	if !workflow {
		// A chore has no precondition: preconditionScript is a workflow's.
		if agentDef.PreconditionScript != "" {
			fmt.Printf("Ignoring %s's preconditionScript: only a workflow's runs.\n", agentDef.Name)
		}
		inputs := map[string]string{
			"prompt":     agentDef.Prompt,
			"agent_name": agentDef.Name,
			"agent_file": agentPath,
			"skip_pr":    strconv.FormatBool(agentDef.SkipPR),
			"labels":     resolvePRLabels(cfg, nil, false),
		}
		return runRecipeIn(ctx, ensure, recipe.AgentRecipe, flags.URL, flags.RunName, "", "", false,
			applyMode{on: true, verb: "open-pr", asUser: rootFlags.User != ""}, inputs, nil)
	}

	// A workflow's prompt carries its issue: the title, the description
	// and the trusted authors' comments, whose references are the related
	// issues its precondition checks.
	fmt.Printf("Fetching details for #%d from GitHub...\n", targetNum)
	issue, _, err := ghClient.Issues.Get(ctx, owner, repo, targetNum)
	if err != nil {
		return fmt.Errorf("fetching issue details: %w", err)
	}
	fmt.Printf("Fetching comments for #%d from GitHub...\n", targetNum)
	comments, err := repoClient.ListIssueComments(ctx, targetNum)
	if err != nil {
		return fmt.Errorf("fetching comments: %w", err)
	}
	var commentMsgs, commentBodies []string
	for _, c := range trustedIssueComments(comments, trustedLogins(cfg)) {
		commentMsgs = append(commentMsgs, fmt.Sprintf("Comment from %s:\n%s", c.GetUser().GetLogin(), c.GetBody()))
		commentBodies = append(commentBodies, c.GetBody())
	}
	var related []string
	for _, n := range common.ExtractRelatedIssuesAndPRs(issue.GetBody(), commentBodies, targetNum) {
		related = append(related, strconv.Itoa(n))
	}
	inputs := map[string]string{
		"prompt": fmt.Sprintf("%s\n\nOriginal GitHub Context:\nTitle: %s\n\nDescription:\n%s\n\nComments:\n%s",
			agentDef.Prompt, issue.GetTitle(), issue.GetBody(), strings.Join(commentMsgs, "\n\n")),
		"agent_name":     agentDef.Name,
		"agent_file":     agentPath,
		"session_id":     flags.SessionID,
		"related_issues": strings.Join(related, ","),
		"precondition":   agentDef.PreconditionScript,
	}
	// A workflow's result is the issues it opens and its journal: no task
	// output to apply.
	return runRecipeIn(ctx, ensure, recipe.WorkflowRecipe, flags.URL, flags.RunName, "", "", false, applyMode{}, inputs, nil)
}

// isWorkflow reports whether a definition is a workflow (mode: workflow),
// which takes the next step of a multi-step process on an issue, rather
// than a chore, which runs on the repository.
func isWorkflow(def *common.AgentDefinition) bool {
	return def.Mode == "workflow"
}
