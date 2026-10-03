package commands

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/constants"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/envd"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/recipe"
	factorysandbox "github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/sandbox"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/spool"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/taskoutput"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/tasks"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/usagereport"
)

type TriageFlags struct {
	IssueURL     string
	Publish      string
	Instructions []string
	// Recipe runs the task as the triage recipe (pkg/recipe) instead of
	// triage_issue.sh: an agent session in the sandbox. Experimental.
	Recipe bool
}

// NewTriageCommand triages a GitHub issue in a sandbox: it produces
// structured suggestions (labels, priority, duplicates, assessment). With
// --publish no (the default) nothing is written to GitHub — the suggestions
// are only printed between the ISSUE TRIAGE banners for the caller to use.
func NewTriageCommand(ctx context.Context) *cobra.Command {
	var flags TriageFlags

	cmd := &cobra.Command{
		Use:   "triage",
		Short: "Triage a GitHub issue in a sandbox pod",
		Example: `  # Draft triage suggestions (printed, nothing posted)
  factory triage --url https://github.com/owner/repo/issues/123

  # Triage and apply the suggested labels + post the assessment as a comment
  factory triage --url https://github.com/owner/repo/issues/123 --publish yes`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := ResolveRootFlags(cmd)
			if err != nil {
				return err
			}
			if flags.IssueURL == "" {
				return fmt.Errorf("--url is required")
			}
			flags.Publish = strings.ToLower(strings.TrimSpace(flags.Publish))
			if flags.Publish != "no" && flags.Publish != "yes" {
				return fmt.Errorf("--publish must be one of: no, yes")
			}

			if rootFlags.Timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, rootFlags.Timeout)
				defer cancel()
			}
			return runTriage(ctx, flags.IssueURL, flags.Publish, flags.Instructions, flags.Recipe, rootFlags.EphemeralStorage, rootFlags.ResolvedSecrets)
		},
	}

	cmd.Flags().StringVar(&flags.IssueURL, "url", "", "GitHub issue URL (e.g. https://github.com/owner/repo/issues/123)")
	cmd.Flags().StringVar(&flags.Publish, "publish", "no", "Publish policy: no (print only) or yes (apply labels and comment)")
	cmd.Flags().StringSliceVar(&flags.Instructions, "instruction", nil, "Triage instruction (local file path, repo file path, or raw string). Can be specified multiple times.")
	cmd.Flags().BoolVar(&flags.Recipe, "recipe", false, "Run as the triage recipe: steps around one agent session (experimental; needs a sandbox image with `factory recipe`)")
	_ = cmd.Flags().MarkHidden("recipe")

	return cmd
}

func runTriage(ctx context.Context, issueURL, publishPolicy string, instructionPaths []string, useRecipe bool, ephemeralStorage string, secrets []factorysandbox.SecretMount) error {
	fmt.Printf("Resolving issue URL: %s...\n", issueURL)

	u, err := url.Parse(issueURL)
	if err != nil {
		return fmt.Errorf("invalid issue URL: %w", err)
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(parts) < 4 || parts[2] != "issues" {
		return fmt.Errorf("expected URL format https://github.com/owner/repo/issues/123, got %s", issueURL)
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
	for _, instVal := range instructionPaths {
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
	// The issue's sandbox, the one plan and fix use: triage leaves the
	// checkout warm for them.
	fmt.Printf("Ensuring the sandbox for issue #%d...\n", issueNum)
	sandboxName, err := factorysandbox.EnsureFixSandbox(ctx, kubeClient, rootFlags.Namespace, repo, strconv.Itoa(issueNum), cloneURL, issue.GetHTMLURL(), issue.GetTitle(), rootFlags.Image, rootFlags.DiskSize, rootFlags.StorageClass, ephemeralStorage, secrets, rootFlags.ResolvedEnvs, rootFlags.User)
	if err != nil {
		return fmt.Errorf("ensuring the issue's sandbox: %w", err)
	}

	secret, err := kubeClient.Clientset.CoreV1().Secrets(rootFlags.Namespace).Get(ctx, rootFlags.SecretName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("fetching %s secret in namespace %s: %w (make sure to run 'factory user onboard' first)", rootFlags.SecretName, rootFlags.Namespace, err)
	}
	githubLogin := string(secret.Data[constants.KeyGithubLogin])
	githubEmail := string(secret.Data[constants.KeyGithubEmail])
	userToken := string(secret.Data[constants.KeyGithubToken])
	if userToken != "" {
		ghClient = github.NewClientWithToken(ctx, userToken)
	}

	fmt.Printf("Connecting to sandbox %s via envd...\n", sandboxName)
	client, err := envd.Connect(ctx, rootFlags.Namespace, sandboxName)
	if err != nil {
		return fmt.Errorf("connecting to sandbox: %w", err)
	}
	defer client.Close()
	if err := refuseIfBusy(ctx, client, sandboxName); err != nil {
		return err
	}

	taskDir := fmt.Sprintf("/workspaces/tasks/triage-%s", time.Now().Format("20060102-150405"))
	promptPath := fmt.Sprintf("%s/agent-prompt.txt", taskDir)
	// The file the task leaves its triage in: the recipe names its output;
	// triage_issue.sh writes triage-output.txt.
	outputFile := "triage-output.txt"
	var cmdStr string
	var recipeBytes []byte
	var recipeInputs map[string]string
	var task spool.Task
	if useRecipe {
		var decl *taskoutput.Decl
		if recipeBytes, recipeInputs, decl, err = triageRecipe(issue, instructions); err != nil {
			return err
		}
		outputFile = decl.From
		task = newSpoolTask("triage", "", issueURL)
		task.Output = decl
		taskDir = spool.TaskDir(task.ID)
	} else {
		if cmdStr, err = writeTriageScript(ctx, client, taskDir, promptPath, issue, instructions); err != nil {
			return err
		}
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
		"ISSUE_NUMBER":               strconv.Itoa(issueNum),
	}
	if err := applyEngineEnv(envMap, secret); err != nil {
		return err
	}

	_ = factorysandbox.MarkSandboxSideTaskRunning(ctx, kubeClient, rootFlags.Namespace, sandboxName, "triage", rootFlags.Engine)
	if useRecipe {
		fmt.Printf("Running the triage recipe (task %s)...\n", task.ID)
		err = spoolRecipe(ctx, client, sandboxName, task, recipeBytes, recipeInputs, envMap)
	} else {
		fmt.Println("Running triage task via envd...")
		err = client.RunTaskResilient(ctx, cmdStr, envMap, taskDir, rootFlags.Detached, rootFlags.AbortOnCancel)
	}
	if err != nil {
		_ = factorysandbox.UpdateSandboxSideTaskAnnotation(ctx, kubeClient, rootFlags.Namespace, sandboxName, "triage", "Failed")
		return fmt.Errorf("running task: %w", err)
	}
	if rootFlags.Detached {
		return nil
	}
	_ = factorysandbox.UpdateSandboxSideTaskAnnotation(ctx, kubeClient, rootFlags.Namespace, sandboxName, "triage", "Completed")

	usagereport.HarvestTask(ctx, client, taskDir, usagereport.Meta{
		Repo:     owner + "/" + repo,
		TaskType: "triage",
		Sandbox:  sandboxName,
		Issues:   []int{issueNum},
	})

	fmt.Println("\nTriage execution completed. Reading output...")

	var stdoutBuf bytes.Buffer
	var stderrBuf bytes.Buffer
	catCmd := fmt.Sprintf("cat %s/%s", taskDir, outputFile)
	if err := client.Exec(ctx, catCmd, "/workspaces", nil, nil, &stdoutBuf, &stderrBuf); err != nil {
		return fmt.Errorf("reading triage output from sandbox: %w (stderr: %s)", err, stderrBuf.String())
	}

	triageOutput := strings.TrimSpace(stdoutBuf.String())
	if triageOutput == "" {
		return fmt.Errorf("triage output was empty")
	}
	triageOutput = taskoutput.CleanAgentYAML(triageOutput, "triage:")

	source := taskoutput.Source{Sandbox: sandboxName, Task: path.Base(taskDir), Engine: rootFlags.Engine}
	if useRecipe {
		source.Recipe = "triage"
	}
	doc, err := taskoutput.Wrap("Triage", triageOutput, taskoutput.Target{URL: issue.GetHTMLURL()}, source)
	if err != nil {
		return fmt.Errorf("parsing structured triage output: %w", err)
	}

	fmt.Println("\n================= ISSUE TRIAGE =================")
	fmt.Println(triageOutput)
	fmt.Println("================================================")

	// The task output, for `factory sandbox task output | factory apply`
	// later: the recipe's runner writes it too, but the classic task and
	// sandboxes older than the runner's do not.
	if out, err := taskoutput.Marshal(doc); err == nil {
		if err := client.WriteFile(ctx, taskDir+"/"+taskoutput.File, out); err != nil {
			klog.Warningf("Could not write %s: %v", taskoutput.File, err)
		}
	}

	if publishPolicy == "yes" {
		if err := taskoutput.Apply(ctx, ghClient, doc, false, os.Stdout); err != nil {
			return err
		}
		fmt.Println("Triage published to the issue.")
	}

	return nil
}

// writeTriageScript puts the classic task into the sandbox: the rendered
// prompt and triage_issue.sh. It returns the command that runs it.
func writeTriageScript(ctx context.Context, client *envd.Client, taskDir, promptPath string, issue *githubv39.Issue, instructions []string) (string, error) {
	promptBytes, err := tasks.RenderStructuredTriagePrompt(tasks.StructuredTriageParams{
		Issue:        *issue,
		Instructions: instructions,
		HTMLURL:      issue.GetHTMLURL(),
	})
	if err != nil {
		return "", fmt.Errorf("rendering structured triage prompt: %w", err)
	}
	scriptBytes, err := tasks.GetTriageScript()
	if err != nil {
		return "", fmt.Errorf("getting triage script: %w", err)
	}
	scriptPath := fmt.Sprintf("%s/pre-script.sh", taskDir)
	fmt.Println("Writing prompt and script into sandbox...")
	if err := client.WriteFile(ctx, promptPath, promptBytes); err != nil {
		return "", fmt.Errorf("writing prompt: %w", err)
	}
	if err := client.WriteFile(ctx, scriptPath, scriptBytes); err != nil {
		return "", fmt.Errorf("writing script: %w", err)
	}
	return fmt.Sprintf("bash -c 'set -o pipefail; bash %s'", scriptPath), nil
}

// instructionSeparator is between instructions joined into one input.
const instructionSeparator = "\n\n---\n\n"

// triageRecipe is the triage recipe, its inputs for issue, and the file
// it leaves the triage in.
func triageRecipe(issue *githubv39.Issue, instructions []string) ([]byte, map[string]string, *taskoutput.Decl, error) {
	recipeBytes, rec, err := recipe.Builtin("triage")
	if err != nil {
		return nil, nil, nil, err
	}
	if rec.TaskOutput == nil || rec.TaskOutput.Kind != "Triage" {
		return nil, nil, nil, fmt.Errorf("the triage recipe must declare a Triage task-output")
	}
	return recipeBytes, map[string]string{
		"issue_url":    issue.GetHTMLURL(),
		"issue_number": strconv.Itoa(issue.GetNumber()),
		"issue_title":  issue.GetTitle(),
		"issue_body":   issue.GetBody(),
		"instructions": strings.Join(instructions, instructionSeparator),
	}, rec.TaskOutput, nil
}

// refuseIfBusy fails when a task is waiting or running in the sandbox. An
// issue's triage, recipes, plan and fix share its sandbox, and two agents
// in one workspace would trip over each other.
func refuseIfBusy(ctx context.Context, client *envd.Client, sandboxName string) error {
	entries, err := spool.List(ctx, client)
	if err != nil {
		return fmt.Errorf("listing the tasks in sandbox %s: %w", sandboxName, err)
	}
	for _, e := range entries {
		switch e.State {
		case spool.Pending, spool.Claimed, spool.Running:
			return fmt.Errorf("sandbox %s is busy: task %s is %s; try again when it is done (factory sandbox task list %s -n %s)", sandboxName, e.ID, e.State, sandboxName, rootFlags.Namespace)
		}
	}
	return nil
}
