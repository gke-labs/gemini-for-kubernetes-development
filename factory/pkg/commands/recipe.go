package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/constants"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/envd"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/recipe"
	factorysandbox "github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/sandbox"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/tasks"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/usagereport"
)

// NewRecipeCommand groups the recipe runner's commands.
func NewRecipeCommand(ctx context.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:    "recipe",
		Short:  "Run tasks as recipes: steps around one agent session (experimental)",
		Hidden: true,
	}
	cmd.AddCommand(newRecipeRunCommand(ctx))
	cmd.AddCommand(newRecipeExecCommand(ctx))
	return cmd
}

// newRecipeRunCommand runs any recipe against an issue or a PR: a new task
// is a YAML file, no Go. It prints what the recipe captured and publishes
// nothing; the dedicated commands (triage, fix …) still do that.
func newRecipeRunCommand(ctx context.Context) *cobra.Command {
	var recipeArg, itemURL string
	var inputArgs []string
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run a recipe against a GitHub issue or PR in a sandbox",
		Example: `  # A built-in recipe
  factory recipe run --recipe triage --url https://github.com/owner/repo/issues/123

  # A recipe file, with an input it declares
  factory recipe run --recipe ./explain-pr.yaml --url https://github.com/owner/repo/pull/45 --input focus="error handling"`,
		RunE: func(c *cobra.Command, _ []string) error {
			if _, err := ResolveRootFlags(c); err != nil {
				return err
			}
			if rootFlags.Timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, rootFlags.Timeout)
				defer cancel()
			}
			overrides, err := parseInputArgs(inputArgs)
			if err != nil {
				return err
			}
			return runRecipe(ctx, recipeArg, itemURL, overrides)
		},
	}
	cmd.Flags().StringVar(&recipeArg, "recipe", "", "A built-in recipe's name, or a recipe file (a path, or a name ending in .yaml)")
	cmd.Flags().StringVar(&itemURL, "url", "", "GitHub issue or PR URL")
	cmd.Flags().StringArrayVar(&inputArgs, "input", nil, "An input as name=value; overrides what the URL sets. Repeatable.")
	_ = cmd.MarkFlagRequired("recipe")
	_ = cmd.MarkFlagRequired("url")
	return cmd
}

// loadRecipe reads a built-in recipe by name, or a file when arg looks
// like one.
func loadRecipe(arg string) ([]byte, *recipe.Recipe, error) {
	if !strings.ContainsRune(arg, '/') && !strings.HasSuffix(arg, ".yaml") && !strings.HasSuffix(arg, ".yml") {
		return recipe.Builtin(arg)
	}
	data, err := os.ReadFile(arg)
	if err != nil {
		return nil, nil, err
	}
	rec, err := recipe.Parse(data)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", arg, err)
	}
	return data, rec, nil
}

func parseInputArgs(args []string) (map[string]string, error) {
	out := map[string]string{}
	for _, a := range args {
		k, v, ok := strings.Cut(a, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("--input %q: want name=value", a)
		}
		out[k] = v
	}
	return out, nil
}

// githubItem is an issue or PR URL taken apart.
type githubItem struct {
	Owner, Repo string
	Number      int
	IsPR        bool
}

func parseGitHubItemURL(raw string) (githubItem, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return githubItem{}, fmt.Errorf("invalid URL: %w", err)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if u.Host != "github.com" || len(parts) < 4 || (parts[2] != "issues" && parts[2] != "pull") {
		return githubItem{}, fmt.Errorf("expected https://github.com/owner/repo/issues/N or …/pull/N, got %s", raw)
	}
	n, err := strconv.Atoi(parts[3])
	if err != nil || n <= 0 {
		return githubItem{}, fmt.Errorf("invalid number in URL: %s", parts[3])
	}
	return githubItem{Owner: parts[0], Repo: parts[1], Number: n, IsPR: parts[2] == "pull"}, nil
}

// issueInputs and prInputs are what every recipe gets from its URL.
func issueInputs(it githubItem, issue *githubv39.Issue) map[string]string {
	return map[string]string{
		"repo_owner":   it.Owner,
		"repo_name":    it.Repo,
		"url":          issue.GetHTMLURL(),
		"issue_url":    issue.GetHTMLURL(),
		"issue_number": strconv.Itoa(it.Number),
		"issue_title":  issue.GetTitle(),
		"issue_body":   issue.GetBody(),
	}
}

func prInputs(it githubItem, pr *githubv39.PullRequest) map[string]string {
	return map[string]string{
		"repo_owner": it.Owner,
		"repo_name":  it.Repo,
		"url":        pr.GetHTMLURL(),
		"pr_url":     pr.GetHTMLURL(),
		"pr_number":  strconv.Itoa(it.Number),
		"pr_title":   pr.GetTitle(),
		"pr_body":    pr.GetBody(),
		"pr_head":    pr.GetHead().GetRef(),
		"pr_base":    pr.GetBase().GetRef(),
	}
}

func runRecipe(ctx context.Context, recipeArg, itemURL string, overrides map[string]string) error {
	recipeBytes, rec, err := loadRecipe(recipeArg)
	if err != nil {
		return err
	}
	it, err := parseGitHubItemURL(itemURL)
	if err != nil {
		return err
	}

	ghClient, err := github.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("creating github client: %w", err)
	}
	var standard map[string]string
	var htmlURL string
	if it.IsPR {
		pr, _, err := ghClient.PullRequests.Get(ctx, it.Owner, it.Repo, it.Number)
		if err != nil {
			return fmt.Errorf("fetching PR #%d: %w", it.Number, err)
		}
		standard, htmlURL = prInputs(it, pr), pr.GetHTMLURL()
	} else {
		issue, _, err := ghClient.Issues.Get(ctx, it.Owner, it.Repo, it.Number)
		if err != nil {
			return fmt.Errorf("fetching issue #%d: %w", it.Number, err)
		}
		standard, htmlURL = issueInputs(it, issue), issue.GetHTMLURL()
	}
	inputs, err := rec.ResolveInputs(standard, overrides)
	if err != nil {
		return err
	}

	kubeClient, err := clients.NewKubernetesClient()
	if err != nil {
		return fmt.Errorf("creating k8s client: %w", err)
	}
	secret, err := kubeClient.Clientset.CoreV1().Secrets(rootFlags.Namespace).Get(ctx, rootFlags.SecretName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("fetching %s secret in namespace %s: %w (make sure to run 'factory user onboard' first)", rootFlags.SecretName, rootFlags.Namespace, err)
	}

	cloneURL := fmt.Sprintf("https://github.com/%s/%s.git", it.Owner, it.Repo)
	fmt.Printf("Ensuring recipe sandbox for #%d...\n", it.Number)
	sandboxName, err := factorysandbox.EnsureRecipeSandbox(ctx, kubeClient, rootFlags.Namespace, it.Repo, it.Number, cloneURL, htmlURL, rootFlags.Image, rootFlags.DiskSize, rootFlags.StorageClass, rootFlags.EphemeralStorage, rootFlags.ResolvedSecrets, rootFlags.ResolvedEnvs, rootFlags.User)
	if err != nil {
		return fmt.Errorf("ensuring recipe sandbox: %w", err)
	}

	fmt.Printf("Connecting to sandbox %s via envd...\n", sandboxName)
	client, err := envd.Connect(ctx, rootFlags.Namespace, sandboxName)
	if err != nil {
		return fmt.Errorf("connecting to sandbox: %w", err)
	}
	defer client.Close()

	taskDir := fmt.Sprintf("/workspaces/tasks/recipe-%s-%s", rec.Name, time.Now().Format("20060102-150405"))
	cmdStr, err := writeRecipe(ctx, client, taskDir, recipeBytes, inputs)
	if err != nil {
		return err
	}

	githubLogin := string(secret.Data[constants.KeyGithubLogin])
	envMap := map[string]string{
		"HOME":                       "/workspaces/.home",
		"GITHUB_TOKEN":               string(secret.Data[constants.KeyGithubToken]),
		"GEMINI_CLI_TRUST_WORKSPACE": "true",
		"REPO_NAME":                  it.Repo,
		"CLONE_URL":                  cloneURL,
		"GITHUB_USER_ID":             githubLogin,
		"GITHUB_USER_EMAIL":          string(secret.Data[constants.KeyGithubEmail]),
		"GITHUB_USER_NAME":           githubLogin,
	}
	// What lib.sh's checkout functions read.
	if it.IsPR {
		envMap["PR_NUMBER"] = strconv.Itoa(it.Number)
	} else {
		envMap["ISSUE_NUMBER"] = strconv.Itoa(it.Number)
	}
	if err := applyEngineEnv(envMap, secret); err != nil {
		return err
	}

	fmt.Printf("Running recipe %s via envd (task dir %s)...\n", rec.Name, taskDir)
	_ = factorysandbox.MarkSandboxTaskRunning(ctx, kubeClient, rootFlags.Namespace, sandboxName, "recipe", rootFlags.Engine)
	if err := client.RunTaskResilient(ctx, cmdStr, envMap, taskDir, rootFlags.Detached, rootFlags.AbortOnCancel); err != nil {
		_ = factorysandbox.UpdateSandboxTaskAnnotation(ctx, kubeClient, rootFlags.Namespace, sandboxName, "recipe", "Failed")
		return fmt.Errorf("running recipe: %w", err)
	}
	if rootFlags.Detached {
		return nil
	}
	_ = factorysandbox.UpdateSandboxTaskAnnotation(ctx, kubeClient, rootFlags.Namespace, sandboxName, "recipe", "Completed")

	meta := usagereport.Meta{Repo: it.Owner + "/" + it.Repo, TaskType: "recipe-" + rec.Name, Sandbox: sandboxName}
	if it.IsPR {
		meta.PR = it.Number
	} else {
		meta.Issue = it.Number
	}
	usagereport.HarvestTask(ctx, client, taskDir, meta)

	for _, name := range rec.Captures() {
		var out, errOut bytes.Buffer
		if err := client.Exec(ctx, "cat "+taskDir+"/"+name, "/workspaces", nil, nil, &out, &errOut); err != nil {
			return fmt.Errorf("reading %s from sandbox: %w (stderr: %s)", name, err, errOut.String())
		}
		fmt.Printf("\n================= %s =================\n%s\n", name, strings.TrimSpace(out.String()))
	}
	fmt.Printf("\nRecipe %s completed. Step logs and the session transcript: %s:%s\n", rec.Name, sandboxName, taskDir)
	return nil
}

// writeRecipe puts a recipe and its inputs into the sandbox and returns
// the command that runs them. The recipe comes from this binary or the
// caller's file; what its steps mean comes from the sandbox's binary.
func writeRecipe(ctx context.Context, client *envd.Client, taskDir string, recipeBytes []byte, inputs map[string]string) (string, error) {
	inputsJSON, err := json.Marshal(inputs)
	if err != nil {
		return "", err
	}
	recipePath, inputsPath := taskDir+"/recipe.yaml", taskDir+"/inputs.json"
	fmt.Println("Writing the recipe into sandbox...")
	if err := client.WriteFile(ctx, recipePath, recipeBytes); err != nil {
		return "", fmt.Errorf("writing recipe: %w", err)
	}
	if err := client.WriteFile(ctx, inputsPath, inputsJSON); err != nil {
		return "", fmt.Errorf("writing inputs: %w", err)
	}
	return fmt.Sprintf("factory recipe exec --recipe %s --inputs %s --task-dir %s", recipePath, inputsPath, taskDir), nil
}

// newRecipeExecCommand runs a recipe inside a sandbox. The CLI starts it
// through envd where it would start a task script, with the same
// environment, so detaching, the log and restart recovery are unchanged.
func newRecipeExecCommand(ctx context.Context) *cobra.Command {
	var recipePath, inputsPath, taskDir string
	cmd := &cobra.Command{
		Use:   "exec",
		Short: "Run a recipe in this sandbox (started by the CLI, not by hand)",
		RunE: func(c *cobra.Command, _ []string) error {
			return runRecipeExec(c.Context(), recipePath, inputsPath, taskDir)
		},
	}
	cmd.Flags().StringVar(&recipePath, "recipe", "", "Recipe file")
	cmd.Flags().StringVar(&inputsPath, "inputs", "", "Inputs file (a JSON object of strings)")
	cmd.Flags().StringVar(&taskDir, "task-dir", "", "Task directory: step logs, the session transcript, captured replies")
	_ = cmd.MarkFlagRequired("recipe")
	_ = cmd.MarkFlagRequired("task-dir")
	return cmd
}

func runRecipeExec(ctx context.Context, recipePath, inputsPath, taskDir string) error {
	data, err := os.ReadFile(recipePath)
	if err != nil {
		return err
	}
	rec, err := recipe.Parse(data)
	if err != nil {
		return err
	}
	inputs := map[string]string{}
	if inputsPath != "" {
		b, err := os.ReadFile(inputsPath)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(b, &inputs); err != nil {
			return fmt.Errorf("parsing inputs: %w", err)
		}
	}
	repoName := os.Getenv("REPO_NAME")
	if repoName == "" {
		return fmt.Errorf("REPO_NAME is not set")
	}
	repoDir := filepath.Join("/workspaces", repoName)
	stepScript, err := tasks.GetRecipeStepScript()
	if err != nil {
		return err
	}

	engine := os.Getenv("ENGINE")
	if engine == "" {
		engine = "gemini"
	}
	// The first model only: runEngine's fallback down the list is not
	// here yet.
	model := ""
	if models := strings.Fields(os.Getenv("MODELS")); len(models) > 0 && models[0] != "default" {
		model = models[0]
	}

	r := &recipe.Runner{
		Exec: &recipe.SandboxExecutor{
			StepScript: stepScript,
			TaskDir:    taskDir,
			RepoDir:    repoDir,
			Env:        os.Environ(),
		},
		StartSession: func(ctx context.Context) (recipe.Session, error) {
			return recipe.StartACPSession(ctx, engine, model, os.Getenv("GEMINI_API_KEY"), repoDir, taskDir)
		},
		TaskDir: taskDir,
		Inputs:  inputs,
		Log:     os.Stdout,
	}
	return r.Run(ctx, rec)
}
