package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math/rand"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/constants"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/envd"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/recipe"
	factorysandbox "github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/sandbox"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/spool"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/taskapi"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/taskoutput"
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
	cmd.AddCommand(newRecipeReviseCommand(ctx))
	for _, name := range recipe.BuiltinNames() {
		// run, exec and revise are taken; TestBuiltinRecipeCommands keeps
		// them so.
		cmd.AddCommand(newBuiltinRecipeCommand(ctx, name))
	}
	return cmd
}

// recipeRunFlags are the flags every way of running a recipe takes.
type recipeRunFlags struct {
	itemURL, runName, session string
	inputArgs                 []string
	apply, dryRun             bool
	// instructions are the values of each instructions-type input's flag,
	// resolved once the repository is known.
	instructions map[string]*[]string
}

func (f *recipeRunFlags) add(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.itemURL, "url", "", "GitHub issue, PR or repository URL")
	cmd.Flags().StringVar(&f.session, "session", "", "With a repository URL: the conversation the run is, which names its sandbox (made if missing). Default: the run name, else a new one")
	cmd.Flags().StringArrayVar(&f.inputArgs, "input", nil, "An input as name=value; overrides what the URL sets. Repeatable.")
	cmd.Flags().StringVar(&f.runName, "run-name", "", "Names this run: running again with the same name follows that task, or returns or applies its result, instead of starting another; find it with sandbox task status|output|attach --run-name")
	cmd.Flags().BoolVar(&f.dryRun, "dry-run", false, "With --apply: print what would be written to GitHub, write nothing, and leave the result to apply")
	cmd.Flags().BoolVar(&f.apply, "apply", false, "Wait for the task's result and apply it to GitHub, as factory apply does. Interrupting stops the waiting, not the task; running the same command again waits for that task, or applies its result if it has finished.")
	_ = cmd.MarkFlagRequired("url")
}

// run runs recipeArg with the flags' inputs and extra, which overrides
// them.
func (f *recipeRunFlags) run(ctx context.Context, c *cobra.Command, recipeArg string, extra map[string]string) error {
	if _, err := ResolveRootFlags(c); err != nil {
		return err
	}
	if f.dryRun && !f.apply {
		return fmt.Errorf("--dry-run goes with --apply")
	}
	if f.apply && rootFlags.Detached {
		return fmt.Errorf("--apply waits for the task; it cannot be --detached")
	}
	if rootFlags.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, rootFlags.Timeout)
		defer cancel()
	}
	overrides, err := parseInputArgs(f.inputArgs)
	if err != nil {
		return err
	}
	for k, v := range extra {
		overrides[k] = v
	}
	instructions := map[string][]string{}
	for in, vals := range f.instructions {
		if len(*vals) > 0 {
			instructions[in] = *vals
		}
	}
	return runRecipe(ctx, recipeArg, f.itemURL, f.runName, f.session, applyMode{f.apply, f.dryRun}, overrides, instructions)
}

// newBuiltinRecipeCommand makes a built-in recipe a command of its own,
// `factory recipe <name>`, with a flag for each input it declares: adding
// a recipe file adds the command.
func newBuiltinRecipeCommand(ctx context.Context, name string) *cobra.Command {
	_, rec, err := recipe.Builtin(name)
	if err != nil {
		// Built-ins are parsed by the tests; this is a broken build.
		panic(err)
	}
	f := recipeRunFlags{instructions: map[string]*[]string{}}
	inputs := map[string]*string{}
	cmd := &cobra.Command{
		Use:   name,
		Short: fmt.Sprintf("Run the built-in %s recipe against a GitHub issue, PR or repository", name),
		Example: fmt.Sprintf(`  factory recipe %[1]s --url https://github.com/owner/repo/issues/123
  factory recipe %[1]s --url https://github.com/owner/repo --session my-question`, name),
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			extra := map[string]string{}
			for in, v := range inputs {
				if c.Flags().Changed(inputFlagName(in, rec.Inputs[in])) {
					extra[in] = *v
				}
			}
			return f.run(ctx, c, name, extra)
		},
	}
	f.add(cmd)
	for _, in := range sortedInputNames(rec) {
		decl := rec.Inputs[in]
		if decl.Revise {
			continue // factory recipe revise --input
		}
		if decl.Type == recipe.InstructionsType {
			// An array, not a slice: an instruction's text may have commas.
			f.instructions[in] = cmd.Flags().StringArray(inputFlagName(in, decl), nil, decl.Description+" (a local file, a file in the repository, or the text itself). Repeatable.")
			continue
		}
		// Not marked required: the URL may set it; the recipe says what is
		// missing when it runs.
		inputs[in] = cmd.Flags().String(inputFlagName(in, decl), decl.Default, decl.Description)
	}
	if rec.TaskOutput != nil {
		cmd.Long = fmt.Sprintf("Run the built-in %s recipe against a GitHub issue, PR or repository.\n\nIts result is a %s task output, which `factory apply` acts on.", name, rec.TaskOutput.Kind)
	}
	return cmd
}

// inputFlagName is the flag an input is set with: issue_body →
// --issue-body. An instructions input's flag is singular, as it is given
// once per instruction: instructions → --instruction.
func inputFlagName(input string, decl recipe.Input) string {
	name := strings.ReplaceAll(input, "_", "-")
	if decl.Type == recipe.InstructionsType {
		name = strings.TrimSuffix(name, "s")
	}
	return name
}

// resolveInstructions reads each instruction as `factory pr review
// --instruction` does and joins them as it does.
func resolveInstructions(ctx context.Context, ghClient *githubv39.Client, owner, repo string, vals []string) (string, error) {
	var out []string
	for _, v := range vals {
		content, isFile, err := resolveInstruction(ctx, ghClient, owner, repo, "", v)
		if err != nil {
			return "", err
		}
		if isFile {
			fmt.Printf("Loaded instruction file: %s\n", v)
		} else {
			fmt.Printf("Loaded instruction: %q\n", v)
		}
		out = append(out, content)
	}
	return strings.Join(out, instructionSeparator), nil
}

func sortedInputNames(rec *recipe.Recipe) []string {
	names := make([]string, 0, len(rec.Inputs))
	for n := range rec.Inputs {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// newRecipeRunCommand runs any recipe against an issue or a PR: a new task
// is a YAML file, no Go. It prints the recipe's outputs and publishes
// nothing; the dedicated commands (triage, fix …) still do that.
func newRecipeRunCommand(ctx context.Context) *cobra.Command {
	var recipeArg string
	var f recipeRunFlags
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run a recipe file against a GitHub issue or PR in a sandbox",
		Example: `  # A recipe file, with an input it declares
  factory recipe run --recipe ./explain-pr.yaml --url https://github.com/owner/repo/pull/45 --input focus="error handling"

  # A built-in recipe; the same as: factory recipe triage --url …
  factory recipe run --recipe triage --url https://github.com/owner/repo/issues/123`,
		RunE: func(c *cobra.Command, _ []string) error {
			return f.run(ctx, c, recipeArg, nil)
		},
	}
	cmd.Flags().StringVar(&recipeArg, "recipe", "", "A recipe file (a path, or a name ending in .yaml), or a built-in recipe's name")
	f.add(cmd)
	_ = cmd.MarkFlagRequired("recipe")
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

// githubItem is an issue or PR URL taken apart, or a repository's, with
// Number 0.
type githubItem struct {
	Owner, Repo string
	Number      int
	IsPR        bool
}

// IsRepo reports whether it is a repository, not an issue or PR in one.
func (it githubItem) IsRepo() bool { return it.Number == 0 }

// parseRecipeTarget is what a recipe runs against: an issue, a PR or a
// repository.
func parseRecipeTarget(raw string) (githubItem, error) {
	if it, err := parseGitHubItemURL(raw); err == nil {
		return it, nil
	}
	u, err := url.Parse(raw)
	if err == nil && u.Host == "github.com" && len(strings.Split(strings.Trim(u.Path, "/"), "/")) == 2 {
		if owner, repo, err := parseGitHubRepoURL(raw); err == nil {
			return githubItem{Owner: owner, Repo: repo}, nil
		}
	}
	return githubItem{}, fmt.Errorf("expected https://github.com/owner/repo, …/issues/N or …/pull/N, got %s", raw)
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

// repoInputs are what a recipe run against a repository gets.
func repoInputs(it githubItem, repo *githubv39.Repository) map[string]string {
	return map[string]string{
		"repo_owner": it.Owner,
		"repo_name":  it.Repo,
		"url":        repo.GetHTMLURL(),
		"repo_url":   repo.GetHTMLURL(),
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

// runRecipe runs recipeArg against itemURL. instructions are the raw
// values of instructions-type inputs, each resolved as
// `factory pr review --instruction` resolves its own. With apply it applies
// the task's result, picking up where an interrupted run left off.
func runRecipe(ctx context.Context, recipeArg, itemURL, runName, session string, apply applyMode, overrides map[string]string, instructions map[string][]string) error {
	recipeBytes, rec, err := loadRecipe(recipeArg)
	if err != nil {
		return err
	}
	if apply.on && rec.TaskOutput == nil {
		return fmt.Errorf("recipe %s declares no task output; there is nothing to --apply", rec.Name)
	}
	it, err := parseRecipeTarget(itemURL)
	if err != nil {
		return err
	}
	if session != "" && !it.IsRepo() {
		return fmt.Errorf("--session is for a repository; an issue's or PR's recipes run in its own sandbox")
	}
	if rec.Credentials == recipe.CredentialsClone && !it.IsPR && !it.IsRepo() {
		// The issue's sandbox is its fix's too, whose setup-git leaves
		// the token in gh's hosts.yml for the agent to read.
		return fmt.Errorf("recipe %s is credentials: clone, which an issue's sandbox, shared with its fix, cannot keep; run it on the PR or the repository", rec.Name)
	}

	ghClient, err := github.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("creating github client: %w", err)
	}
	for in, vals := range instructions {
		joined, err := resolveInstructions(ctx, ghClient, it.Owner, it.Repo, vals)
		if err != nil {
			return err
		}
		overrides[in] = joined
	}
	var standard map[string]string
	var htmlURL string
	switch {
	case it.IsRepo():
		repo, _, err := ghClient.Repositories.Get(ctx, it.Owner, it.Repo)
		if err != nil {
			return fmt.Errorf("fetching %s/%s: %w", it.Owner, it.Repo, err)
		}
		standard, htmlURL = repoInputs(it, repo), repo.GetHTMLURL()
	case it.IsPR:
		pr, _, err := ghClient.PullRequests.Get(ctx, it.Owner, it.Repo, it.Number)
		if err != nil {
			return fmt.Errorf("fetching PR #%d: %w", it.Number, err)
		}
		standard, htmlURL = prInputs(it, pr), pr.GetHTMLURL()
	default:
		issue, _, err := ghClient.Issues.Get(ctx, it.Owner, it.Repo, it.Number)
		if err != nil {
			return fmt.Errorf("fetching issue #%d: %w", it.Number, err)
		}
		standard, htmlURL = issueInputs(it, issue), issue.GetHTMLURL()
	}
	// Whether what the agent writes for GitHub says an agent wrote it.
	standard["disclose"] = strconv.FormatBool(rootFlags.Disclose)
	inputs, err := rec.ResolveInputs(standard, overrides)
	if err != nil {
		return err
	}
	if err := rec.CheckRender(inputs); err != nil {
		return fmt.Errorf("recipe %s on %s: %w", rec.Name, htmlURL, err)
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
	// An issue's recipes run in the issue's sandbox, beside its triage,
	// plan and fix; a PR's in a sandbox of their own, one for those that
	// hold the token and one for credentials: clone's, which no token
	// has touched; a repository's in one per conversation, named as a
	// research session's is, since its transcript is the sandbox's.
	var sandboxName string
	switch {
	case it.IsRepo():
		if session == "" {
			session = runName
		}
		if session == "" {
			session = fmt.Sprintf("%s-%s-%04x", rec.Name, time.Now().Format("20060102-150405"), rand.Intn(1<<16))
		}
		fmt.Printf("Ensuring the sandbox for %s/%s, session %s...\n", it.Owner, it.Repo, session)
		sandboxName, err = factorysandbox.EnsureResearchSandbox(ctx, kubeClient, rootFlags.Namespace, it.Repo, session, cloneURL, htmlURL, rootFlags.Image, rootFlags.DiskSize, rootFlags.StorageClass, rootFlags.EphemeralStorage, rootFlags.ResolvedSecrets, rootFlags.ResolvedEnvs, rootFlags.User)
	case it.IsPR:
		// A credentials: clone recipe gets a sandbox of its own, named
		// after it: no recipe holding the token has run there.
		ownRecipe := ""
		if rec.Credentials == recipe.CredentialsClone {
			ownRecipe = rec.Name
		}
		fmt.Printf("Ensuring the sandbox for #%d...\n", it.Number)
		sandboxName, err = factorysandbox.EnsureRecipeSandbox(ctx, kubeClient, rootFlags.Namespace, it.Repo, it.Number, ownRecipe, cloneURL, htmlURL, rootFlags.Image, rootFlags.DiskSize, rootFlags.StorageClass, rootFlags.EphemeralStorage, rootFlags.ResolvedSecrets, rootFlags.ResolvedEnvs, rootFlags.User)
	default:
		fmt.Printf("Ensuring the sandbox for #%d...\n", it.Number)
		sandboxName, err = factorysandbox.EnsureFixSandbox(ctx, kubeClient, rootFlags.Namespace, it.Repo, strconv.Itoa(it.Number), cloneURL, htmlURL, standard["issue_title"], rootFlags.Image, rootFlags.DiskSize, rootFlags.StorageClass, rootFlags.EphemeralStorage, rootFlags.ResolvedSecrets, rootFlags.ResolvedEnvs, rootFlags.User)
	}
	if err != nil {
		return fmt.Errorf("ensuring the sandbox: %w", err)
	}

	fmt.Printf("Connecting to sandbox %s...\n", sandboxName)
	sb, err := taskapi.Connect(ctx, rootFlags.Namespace, sandboxName)
	if err != nil {
		return fmt.Errorf("connecting to sandbox: %w", err)
	}
	defer sb.Close()
	if runName != "" {
		entries, err := sb.List(ctx)
		if err != nil {
			return err
		}
		e, ok, err := runByName(entries, runName, rec.Name, htmlURL)
		if err != nil {
			return err
		}
		if ok {
			return resumeNamedRun(ctx, sb, ghClient, rec, sandboxName, e, apply)
		}
	}
	if apply.on {
		e, ok, err := resumableTask(ctx, sb, rec.Name, htmlURL)
		if err != nil {
			return err
		}
		if ok {
			fmt.Printf("Picking up task %s, this recipe's last run on %s (run again once it is applied to start a new one).\n", e.ID, htmlURL)
			return awaitAndApply(ctx, sb, ghClient, sandboxName, e.ID, "", apply.dryRun)
		}
		// Interrupting stops the waiting; the task runs on, for the
		// next run to pick up.
		rootFlags.AbortOnCancel = false
	}
	if err := refuseIfBusy(ctx, sb, sandboxName); err != nil {
		return err
	}

	envMap, secrets, err := recipeEnv(secret, it, rec.Credentials)
	if err != nil {
		return err
	}
	if it.IsPR {
		// The base branch the clone step fetches beside the PR's head.
		envMap["PR_BASE"] = standard["pr_base"]
	}

	task := newSpoolTask(rec.Name, runName, itemURL)
	if rec.StartOutputs() {
		task.Output = rec.OutputDecl()
	}
	task.TaskType = rec.TaskType
	fmt.Printf("Running recipe %s (task %s)...\n", rec.Name, task.ID)
	if apply.on {
		fmt.Println("Interrupting stops the waiting, not the task: run the same command again to wait for it and apply its result.")
	}
	if done, err := startRecipeTask(ctx, kubeClient, sb, it, rec, task, recipeBytes, inputs, envMap, secrets); err != nil || !done {
		return err
	}
	if apply.on {
		return awaitAndApply(ctx, sb, ghClient, sandboxName, task.ID, "", apply.dryRun)
	}
	printResultHint(rec, task, sandboxName)
	return nil
}

// printResultHint says where a finished task's result is: its task output,
// or for a start that leaves none, the revises that write one.
func printResultHint(rec *recipe.Recipe, task spool.Task, sandboxName string) {
	switch {
	case task.Output != nil:
		fmt.Printf("Its %s result, to look at and apply:\n  factory sandbox task output %s -n %s --task %s | factory apply -f -\n", task.Output.Kind, sandboxName, rootFlags.Namespace, task.ID)
	case rec.TaskOutput != nil:
		for _, rv := range rec.Revise {
			fmt.Printf("%s, once you have talked in its session:\n  factory recipe revise %s %s -n %s\n", rv.Label, sandboxName, rv.ID, rootFlags.Namespace)
		}
	}
}

// recipeEnv is the environment a recipe task on it runs with, and its
// secrets: the member's GitHub identity and engine credentials, from
// their secret. The GitHub token is in the environment, or under
// credentials: clone, the one secret.
func recipeEnv(secret *corev1.Secret, it githubItem, credentials string) (envMap, secrets map[string]string, err error) {
	githubLogin := string(secret.Data[constants.KeyGithubLogin])
	token := string(secret.Data[constants.KeyGithubToken])
	envMap = map[string]string{
		"HOME":                       "/workspaces/.home",
		"GEMINI_CLI_TRUST_WORKSPACE": "true",
		"REPO_NAME":                  it.Repo,
		"CLONE_URL":                  fmt.Sprintf("https://github.com/%s/%s.git", it.Owner, it.Repo),
		"GITHUB_USER_ID":             githubLogin,
		"GITHUB_USER_EMAIL":          string(secret.Data[constants.KeyGithubEmail]),
		"GITHUB_USER_NAME":           githubLogin,
	}
	if credentials == recipe.CredentialsClone {
		secrets = map[string]string{"GITHUB_TOKEN": token}
	} else {
		envMap["GITHUB_TOKEN"] = token
	}
	// What lib.sh's checkout functions read.
	switch {
	case it.IsRepo():
	case it.IsPR:
		envMap["PR_NUMBER"] = strconv.Itoa(it.Number)
	default:
		envMap["ISSUE_NUMBER"] = strconv.Itoa(it.Number)
	}
	if err := applyEngineEnv(envMap, secret); err != nil {
		return nil, nil, err
	}
	return envMap, secrets, nil
}

// recordedRun is the run recorded on the sandbox for task: with the
// recipe's revises, so a session view has its buttons before any output.
func recordedRun(task spool.Task, rec *recipe.Recipe, started time.Time) factorysandbox.RecordedRun {
	run := factorysandbox.RecordedRun{Name: task.RunName, Task: task.ID, Session: task.Session, StartedAt: started}
	for _, rv := range rec.Revise {
		run.Revises = append(run.Revises, factorysandbox.RecordedRevise{ID: rv.ID, Label: rv.Label})
	}
	return run
}

// startRecipeTask records the task on its sandbox and hands it over.
// Unless --detached it follows the task to its end and prints its
// outputs, and returns true.
func startRecipeTask(ctx context.Context, kubeClient *clients.KubernetesClient, sb taskapi.Sandbox, it githubItem, rec *recipe.Recipe, task spool.Task, recipeBytes []byte, inputs, envMap, secrets map[string]string) (bool, error) {
	sandboxName := sb.Name()
	taskType := "recipe-" + rec.Name
	// In an issue's sandbox a recipe is a side task, and last-task-* stay
	// the plan's or fix's, unless it is the sandbox's main task itself.
	side, update := false, factorysandbox.UpdateSandboxTaskAnnotation
	if rec.TaskType != "" {
		taskType = rec.TaskType
	} else if !it.IsPR && !it.IsRepo() {
		side, update = true, factorysandbox.UpdateSandboxSideTaskAnnotation
	}
	_ = factorysandbox.MarkSandboxRunStarted(ctx, kubeClient, rootFlags.Namespace, sandboxName, taskType, rootFlags.Engine, side, recordedRun(task, rec, time.Now().UTC()))
	if err := spoolRecipe(ctx, sb, task, recipeBytes, inputs, envMap, secrets); err != nil {
		_ = update(ctx, kubeClient, rootFlags.Namespace, sandboxName, taskType, "Failed")
		return false, fmt.Errorf("running recipe: %w", err)
	}
	if it.IsRepo() && task.Revise == "" {
		// The sandbox is the conversation's from here: the receipt that
		// keeps EnsureResearchSandbox from taking it for an interrupted
		// launch's and replacing it.
		if err := factorysandbox.MarkResearchReady(ctx, kubeClient, rootFlags.Namespace, sandboxName, rootFlags.Engine); err != nil {
			return false, err
		}
	}
	if rootFlags.Detached {
		return false, nil
	}
	_ = update(ctx, kubeClient, rootFlags.Namespace, sandboxName, taskType, "Completed")

	meta := usagereport.Meta{Repo: it.Owner + "/" + it.Repo, TaskType: taskType, Sandbox: sandboxName}
	switch {
	case it.IsRepo():
	case it.IsPR:
		meta.PR = it.Number
	default:
		meta.Issue = it.Number
	}
	taskDir := spool.TaskDir(task.ID)
	usagereport.HarvestTaskFiles(ctx, taskDir, func(name string) ([]byte, error) { return sb.ReadFile(ctx, task.ID, name) }, meta)

	if err := printRecipeOutputs(ctx, sb, rec, task.ID); err != nil {
		return false, err
	}
	fmt.Printf("\nRecipe %s completed. Step logs and the session transcript: %s:%s\n", rec.Name, sandboxName, taskDir)
	return true, nil
}

func printRecipeOutputs(ctx context.Context, sb taskapi.Sandbox, rec *recipe.Recipe, id string) error {
	for _, name := range rec.OutputFiles() {
		out, err := sb.ReadFile(ctx, id, name)
		if errors.Is(err, os.ErrNotExist) {
			continue // not written by this part: a start before any revise
		}
		if err != nil {
			return fmt.Errorf("reading %s from sandbox: %w", name, err)
		}
		fmt.Printf("\n================= %s =================\n%s\n", name, strings.TrimSpace(string(out)))
	}
	return nil
}

// newSpoolTask names a recipe task: unique, and sortable by when it was
// started.
func newSpoolTask(recipeName, runName, itemURL string) spool.Task {
	now := time.Now()
	return spool.Task{
		ID:          fmt.Sprintf("recipe-%s-%s-%04x", recipeName, now.Format("20060102-150405"), rand.Intn(1<<16)),
		RunName:     runName,
		Recipe:      recipeName,
		URL:         itemURL,
		SubmittedAt: now.UTC(),
	}
}

// spoolRecipe hands a recipe to the sandbox and follows it, or, with
// --detached, returns once the sandbox has started it. A sandbox whose
// image predates the spool never claims it; the recipe is then started
// through envd as before, in the same task directory.
func spoolRecipe(ctx context.Context, sb taskapi.Sandbox, task spool.Task, recipeBytes []byte, inputs, envMap, secrets map[string]string) error {
	recipeBytes, err := recipe.ForSandbox(recipeBytes)
	if err != nil {
		return err
	}
	err = sb.Start(ctx, task, recipeBytes, inputs, envMap, secrets)
	if es, ok := sb.(*taskapi.EnvdSandbox); ok && errors.Is(err, spool.ErrNotClaimed) {
		fmt.Println("The sandbox's image has no spool (recreate the sandbox to get one); starting the recipe through envd instead.")
		taskDir := spool.TaskDir(task.ID)
		cmdStr, err := writeRecipe(ctx, es.Client, taskDir, recipeBytes, inputs)
		if err != nil {
			return err
		}
		// So that task list and attach find it like a spooled one.
		if taskJSON, err := json.Marshal(task); err == nil {
			_ = es.Client.WriteFile(ctx, taskDir+"/"+spool.TaskFile, taskJSON)
		}
		if err := es.RunTaskResilient(ctx, cmdStr, envMap, taskDir, rootFlags.Detached, rootFlags.AbortOnCancel); err != nil || !rootFlags.Detached {
			return err
		}
		printDetachedHint(sb.Name(), task.ID)
		return nil
	}
	if err != nil {
		return err
	}
	if rootFlags.Detached {
		printDetachedHint(sb.Name(), task.ID)
		return nil
	}
	return sb.Attach(ctx, task.ID, envMap, rootFlags.AbortOnCancel)
}

func printDetachedHint(sandboxName, taskID string) {
	fmt.Printf("Task %[1]s started in the sandbox. Follow it, or check on it and read its outputs, with:\n  factory sandbox task attach %[2]s -n %[3]s --task %[1]s\n  factory sandbox task status %[2]s -n %[3]s --task %[1]s\n  factory sandbox task output %[2]s -n %[3]s --task %[1]s\n", taskID, sandboxName, rootFlags.Namespace)
}

// runByName is the task in entries (newest first) run under
// runName, if any. A run name is one run's: of one recipe on one
// item, so finding it under another is the caller's mistake.
func runByName(entries []spool.Entry, runName, recipeName, itemURL string) (spool.Entry, bool, error) {
	for _, e := range entries {
		if e.RunName != runName {
			continue
		}
		if e.Recipe != recipeName || normalizeItemURL(e.URL) != normalizeItemURL(itemURL) {
			return e, false, fmt.Errorf("run name %s is task %s, recipe %s on %s: use another name for this run", runName, e.ID, e.Recipe, e.URL)
		}
		return e, true, nil
	}
	return spool.Entry{}, false, nil
}

// resumeNamedRun is a recipe run whose run name names a task already:
// rather than start another it follows that one, or, if it has ended,
// prints its outputs or applies its result. A failed run stays failed;
// retrying takes a new id.
func resumeNamedRun(ctx context.Context, sb taskapi.Sandbox, gh *githubv39.Client, rec *recipe.Recipe, sandboxName string, e spool.Entry, apply applyMode) error {
	fmt.Printf("Run name %s is task %s (%s): picking it up instead of starting another.\n", e.RunName, e.ID, e.State)
	if apply.on {
		if e.State == spool.Exited && e.ExitCode == "0" && !apply.dryRun && taskApplied(ctx, sb, e.ID) {
			fmt.Printf("Task %s's result is applied already.\n", e.ID)
			return nil
		}
		return awaitAndApply(ctx, sb, gh, sandboxName, e.ID, "", apply.dryRun)
	}
	if e.State != spool.Exited {
		if rootFlags.Detached {
			printDetachedHint(sandboxName, e.ID)
			return nil
		}
		if err := awaitTaskStart(ctx, sb, e); err != nil {
			return err
		}
		if err := sb.Attach(ctx, e.ID, nil, rootFlags.AbortOnCancel); err != nil {
			return fmt.Errorf("task %s: %w", e.ID, err)
		}
	}
	// Finding it again settles the sandbox's record of it, if it ended
	// unwatched.
	sel := taskSelectFlags{id: e.ID}
	e, err := sel.find(ctx, sb)
	if err != nil {
		return err
	}
	if e.State != spool.Exited {
		return fmt.Errorf("task %s is %s; run again to follow it", e.ID, e.State)
	}
	if e.ExitCode != "0" {
		return fmt.Errorf("task %s failed (exit %s); a run name is one run's, so retry under a new one. Its log: factory sandbox task logs %s -n %s --task %s", e.ID, e.ExitCode, sandboxName, rootFlags.Namespace, e.ID)
	}
	if rootFlags.Detached {
		fmt.Printf("Task %s has finished.\n", e.ID)
	}
	if err := printRecipeOutputs(ctx, sb, rec, e.ID); err != nil {
		return err
	}
	if rec.TaskOutput != nil {
		fmt.Printf("Its %s result, to look at and apply:\n  factory sandbox task output %s -n %s --task %s | factory apply -f -\n", rec.TaskOutput.Kind, sandboxName, rootFlags.Namespace, e.ID)
	}
	return nil
}

// applyMode is what --apply and --dry-run ask of a recipe run.
type applyMode struct{ on, dryRun bool }

// resumableTask is the newest run of recipeName on itemURL in the sandbox
// when it is one `--apply` picks up: still pending or running, or
// finished well and not applied yet. Anything else — failed, applied, no
// run — starts a new one.
func resumableTask(ctx context.Context, sb taskapi.Sandbox, recipeName, itemURL string) (spool.Entry, bool, error) {
	entries, err := sb.List(ctx)
	if err != nil {
		return spool.Entry{}, false, err
	}
	e, ok := lastRun(entries, recipeName, itemURL)
	if !ok || e.State != spool.Exited {
		return e, ok, nil
	}
	if e.ExitCode != "0" || taskApplied(ctx, sb, e.ID) {
		return spool.Entry{}, false, nil
	}
	return e, true, nil
}

// lastRun is the newest task in entries (newest first) that ran
// recipeName on itemURL.
func lastRun(entries []spool.Entry, recipeName, itemURL string) (spool.Entry, bool) {
	want := normalizeItemURL(itemURL)
	for _, e := range entries {
		if e.Recipe == recipeName && normalizeItemURL(e.URL) == want {
			return e, true
		}
	}
	return spool.Entry{}, false
}

func taskApplied(ctx context.Context, sb taskapi.Sandbox, id string) bool {
	out, _ := sb.ReadFile(ctx, id, taskoutput.AppliedFile)
	return len(out) > 0
}

// awaitAndApply follows task id to its end, unless it has ended, and
// applies its result with gh, as `factory apply` does: each write it
// offers, or verb's alone. Applying again is harmless: a triage's comment
// is not posted twice.
func awaitAndApply(ctx context.Context, sb taskapi.Sandbox, gh *githubv39.Client, sandboxName, id, verb string, dryRun bool) error {
	sel := taskSelectFlags{id: id}
	e, err := sel.find(ctx, sb)
	if err != nil {
		return err
	}
	if e.State != spool.Exited {
		fmt.Printf("Waiting for task %s to finish; interrupting stops the waiting, not the task...\n", e.ID)
		if err := awaitTaskStart(ctx, sb, e); err != nil {
			return err
		}
		if err := sb.Attach(ctx, e.ID, nil, false); err != nil {
			return fmt.Errorf("task %s: %w", e.ID, err)
		}
		if e, err = sel.find(ctx, sb); err != nil {
			return err
		}
	}
	if e.State != spool.Exited {
		return fmt.Errorf("task %s is %s; run again to wait for it", e.ID, e.State)
	}
	if e.ExitCode != "0" {
		return fmt.Errorf("task %s failed (exit %s); nothing applied. Its log: factory sandbox task logs %s -n %s --task %s", e.ID, e.ExitCode, sandboxName, rootFlags.Namespace, e.ID)
	}
	data, err := readTaskOutput(ctx, sb, e)
	if err != nil {
		return err
	}
	if data == nil {
		return fmt.Errorf("task %s left no task output to apply", e.ID)
	}
	docs, err := taskoutput.Parse(data)
	if err != nil {
		return fmt.Errorf("task %s: %w", e.ID, err)
	}
	fmt.Printf("\nApplying the result of task %s...\n", e.ID)
	for _, d := range docs {
		target := d.Target.URL
		if err := applyDocument(ctx, gh, d, verb, sandboxName, dryRun); err != nil {
			return fmt.Errorf("applying the %s for %s: %w", d.Kind, target, err)
		}
	}
	if dryRun {
		return nil
	}
	if err := sb.WriteFile(ctx, e.ID, taskoutput.AppliedFile, []byte(time.Now().UTC().Format(time.RFC3339)+"\n")); err != nil {
		fmt.Fprintf(os.Stderr, "Applied, but could not mark task %s applied (%v): running this again applies it again, which posts nothing twice.\n", e.ID, err)
	}
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
	// First, so that no failure below leaves them on disk.
	secrets, err := takeSecrets(taskDir)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(recipePath)
	if err != nil {
		return err
	}
	rec, err := recipe.Parse(data)
	if err != nil {
		return err
	}
	if rec.Credentials == recipe.CredentialsClone {
		// The token in the environment is readable by the agent from
		// /proc for as long as this process lives; the caller hands it
		// over as a secret instead.
		for _, k := range recipe.GitHubTokenEnv {
			if os.Getenv(k) != "" {
				return fmt.Errorf("recipe %s is credentials: clone, but the task's environment carries %s; it goes in the task's secrets", rec.Name, k)
			}
		}
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

	task := readSpoolTask(taskDir)
	r := &recipe.Runner{
		Exec: &recipe.SandboxExecutor{
			StepScript:  stepScript,
			TaskDir:     taskDir,
			RepoDir:     repoDir,
			Env:         os.Environ(),
			Credentials: rec.Credentials,
			Secrets:     secrets,
		},
		StartSession: func(ctx context.Context) (recipe.Session, error) {
			apiKey := os.Getenv("GEMINI_API_KEY")
			// Started by a daemon that hosts sessions: the session is the
			// daemon's, so it can be watched and continued after the task.
			if token := os.Getenv(taskapi.EnvTaskToken); token != "" {
				base := fmt.Sprintf("http://127.0.0.1:%d/v1", taskapi.Port)
				// A revise continues the conversation of the task it
				// revises.
				if task.Session != "" {
					return recipe.OpenDaemonSession(ctx, base, token, task.Session, engine, model, apiKey, repoDir)
				}
				return recipe.StartDaemonSession(ctx, base, token, engine, model, apiKey, repoDir, taskDir)
			}
			if task.Session != "" {
				return nil, fmt.Errorf("a revise needs the sandbox's daemon to host sessions; recreate the sandbox on a newer image")
			}
			return recipe.StartACPSession(ctx, engine, model, apiKey, repoDir, taskDir)
		},
		TaskDir: taskDir,
		Inputs:  inputs,
		Revise:  task.Revise,
		Log:     os.Stdout,
	}
	if err := r.Run(ctx, rec); err != nil {
		return err
	}
	return writeTaskOutput(taskDir, repoDir, inputs, engine)
}

// takeSecrets reads the task's secrets (spool.SecretsFile) as KEY=VALUE
// and deletes the file: from here on they are in this process's memory
// only, until the step allowed them has run.
func takeSecrets(taskDir string) ([]string, error) {
	path := filepath.Join(taskDir, spool.SecretsFile)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if rerr := os.Remove(path); rerr != nil && err == nil {
		return nil, fmt.Errorf("removing %s: %w", spool.SecretsFile, rerr)
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", spool.SecretsFile, err)
	}
	m := map[string]string{}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", spool.SecretsFile, err)
	}
	out := make([]string, 0, len(m))
	for _, k := range slices.Sorted(maps.Keys(m)) {
		out = append(out, k+"="+m[k])
	}
	return out, nil
}

// readSpoolTask is the task's task.json, empty for a task started without
// one: which part of the recipe it runs, and whose session.
func readSpoolTask(taskDir string) spool.Task {
	var task spool.Task
	if data, err := os.ReadFile(filepath.Join(taskDir, spool.TaskFile)); err == nil {
		_ = json.Unmarshal(data, &task)
	}
	return task
}

// writeTaskOutput wraps the result the task declared (task.json's output)
// into taskoutput.File. A result that does not parse fails the task: it is
// caught here, not when someone applies it.
func writeTaskOutput(taskDir, repoDir string, inputs map[string]string, engine string) error {
	data, err := os.ReadFile(filepath.Join(taskDir, spool.TaskFile))
	if err != nil {
		return nil // started by hand, or by a CLI older than task.json
	}
	var task spool.Task
	if err := json.Unmarshal(data, &task); err != nil || task.Output == nil {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(taskDir, task.Output.From))
	if err != nil {
		return fmt.Errorf("reading the task's %s result: %w", task.Output.Kind, err)
	}
	if !slices.Contains(taskoutput.KnownKinds(), task.Output.Kind) {
		// A kind newer than this sandbox's image: the client, which knows
		// it, wraps the result when it is read (sandbox task output).
		fmt.Printf("::task-output %s left in %s for the client to wrap\n", task.Output.Kind, task.Output.From)
		return nil
	}
	target := taskTarget(task, inputs)
	target.Commit = prHeadCommit(repoDir)
	doc, err := taskoutput.Wrap(task.Output.Kind, string(raw), target, taskoutput.Source{
		Task:    filepath.Base(taskDir),
		Session: task.Session,
		Recipe:  task.Recipe,
		Engine:  engine,
	})
	if err != nil {
		return err
	}
	if doc.Kind == "Change" {
		if err := fillChange(doc, task, taskDir, inputs); err != nil {
			return err
		}
	}
	doc.Actions = task.Output.Actions
	out, err := taskoutput.Marshal(doc)
	if err != nil {
		return err
	}
	fmt.Printf("::task-output %s %s\n", doc.Kind, taskoutput.File)
	return os.WriteFile(filepath.Join(taskDir, taskoutput.File), out, 0o644)
}

// fillChange puts on a Change what the push step recorded (where the
// commits went, and which they are) rather than what the agent said, and
// the labels the task was given. A revise's Change is about the fix's PR
// (pr_url, which factory recipe revise sets), and keeps the title and
// body the previous one had (pushed_title, pushed_body) where the agent
// wrote none.
func fillChange(doc *taskoutput.Document, task spool.Task, taskDir string, inputs map[string]string) error {
	if task.Revise != "" {
		if u := inputs["pr_url"]; u != "" {
			doc.Target.URL = u
		}
		if err := doc.KeepTitle(inputs["pushed_title"], inputs["pushed_body"]); err != nil {
			return err
		}
	}
	data, err := os.ReadFile(filepath.Join(taskDir, taskoutput.PushedFile))
	if err != nil {
		return fmt.Errorf("the Change has no push: %w", err)
	}
	var p taskoutput.Pushed
	if err := json.Unmarshal(data, &p); err != nil {
		return fmt.Errorf("parsing %s: %w", taskoutput.PushedFile, err)
	}
	if err := doc.SetPushed(p); err != nil {
		return err
	}
	var labels []string
	for _, l := range strings.Split(inputs["labels"], ",") {
		if l = strings.TrimSpace(l); l != "" {
			labels = append(labels, l)
		}
	}
	return doc.AddLabels(labels)
}

// taskTarget is the issue, PR or repository a task's result is about.
func taskTarget(task spool.Task, inputs map[string]string) taskoutput.Target {
	for _, u := range []string{task.URL, inputs["url"], inputs["issue_url"], inputs["pr_url"]} {
		if u != "" {
			return taskoutput.Target{URL: u}
		}
	}
	return taskoutput.Target{}
}

// prHeadCommit is the PR head the clone step checked out
// (refs/factory/pr/head), what a PR's result is of; empty for anything
// else.
func prHeadCommit(repoDir string) string {
	if repoDir == "" {
		return ""
	}
	out, err := exec.Command("git", "-C", repoDir, "rev-parse", "--verify", "-q", "refs/factory/pr/head^{commit}").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// instructionSeparator is between instructions joined into one input.
const instructionSeparator = "\n\n---\n\n"

// refuseIfBusy fails when a task is waiting or running in the sandbox. An
// issue's triage, recipes, plan and fix share its sandbox, and two agents
// in one workspace would trip over each other.
func refuseIfBusy(ctx context.Context, sb taskapi.Sandbox, sandboxName string) error {
	entries, err := sb.List(ctx)
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
