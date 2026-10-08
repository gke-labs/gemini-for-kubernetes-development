package factorycli

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
)

// RecipeOptions are the inputs for one `factory recipe <name>` invocation,
// for any recipe: what it runs on, its inputs, the sandbox it makes, and
// what the runner does with its task output once it ends.
type RecipeOptions struct {
	// Recipe is the built-in recipe's name (factory recipe list).
	Recipe string
	// URL is the issue, PR or repository the recipe runs on.
	URL string
	// SandboxName is the sandbox the run's task output is read back from;
	// empty lets factory find it by URL.
	SandboxName string
	// Session names a repository recipe's sandbox (factory --session),
	// made if missing; empty for an issue's or a PR's.
	Session string
	// Namespace the task (and its sandbox) runs in; factory resolves the
	// task identity from the factory-user Secret in this namespace.
	Namespace string
	// NewSession opens a new conversation of the recipe's session instead
	// of continuing it (factory --new-session).
	NewSession bool
	// Inputs are the recipe's inputs (factory --input name=value).
	Inputs map[string]string
	// Instructions are passed as repeated --instruction flags, which
	// factory reads as files or text, for a recipe's instructions input.
	Instructions []string
	// Image overrides the sandbox base image; must be factory-compatible.
	Image string
	// WorkspaceDiskSize overrides the workspace PVC size.
	WorkspaceDiskSize string
	// GithubToken authenticates factory's host-side GitHub reads, and the
	// write of Apply.
	GithubToken string
	// Engine selects the agent engine (factory --engine); empty = gemini.
	Engine string
	// Disclose is factory's --disclose: whether what the agent writes says
	// an agent wrote it. Always passed, because factory defaults it on.
	Disclose bool
	// Timeout bounds the child process; the in-sandbox task itself is not
	// killed on timeout (--abort-on-cancel=false) and is followed by the
	// next invocation, by its run name. Zero is 30 minutes.
	Timeout time.Duration
	// RunName records the run's task in the sandbox, to read its result
	// back by (factory --run-name). Invoked again with it, factory follows
	// the run or reads its result, and refuses a busy sandbox.
	RunName string
	// Detached returns once the sandbox has the task (factory --detached):
	// the run goes on in the sandbox, and the result has no task output.
	Detached bool
	// Apply is the action applied to the task output when the run ends
	// (factory apply --action): a fix's open-pr, a review's post-review.
	// Empty applies nothing.
	Apply string
}

// StartRecipe runs `factory recipe <name>` and, unless Detached, reads its
// task output back with `factory sandbox task output --run-name`, then
// applies Apply to it. The result's Output has the task output between
// taskOutputBanner and a closer (HarvestedTaskOutput), then what apply
// said; with Apply, a result without an error is the action applied.
func (r *Runner) StartRecipe(key string, opts RecipeOptions) bool {
	timeout := recipeTimeout(opts.Timeout)
	var pre *preflight
	if !opts.Detached {
		sandbox := opts.SandboxName
		if sandbox == "" {
			sandbox = opts.URL
		}
		pre = r.harvest(sandbox, opts.Namespace, opts.RunName, opts.Apply, opts.GithubToken)
	}
	// No probe: the run name is the run.
	return r.startWithPreflight(key, recipeArgs(opts, timeout), opts.GithubToken, timeout, pre)
}

func recipeTimeout(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		return 30 * time.Minute
	}
	return timeout
}

// recipeArgs is the command line, split out so the contract with factory
// can be asserted without spawning anything.
func recipeArgs(opts RecipeOptions, timeout time.Duration) []string {
	args := []string{
		"recipe", opts.Recipe,
		"--run-name", opts.RunName,
		"--url", opts.URL,
		"--namespace", opts.Namespace,
		"--timeout", timeout.String(),
		// Never kill the in-sandbox task when this process dies; the next
		// invocation follows it instead.
		"--abort-on-cancel=false",
	}
	if opts.Session != "" {
		args = append(args, "--session", opts.Session)
	}
	if opts.Detached {
		args = append(args, "--detached")
	}
	if opts.NewSession {
		args = append(args, "--new-session")
	}
	for _, name := range slices.Sorted(maps.Keys(opts.Inputs)) {
		args = append(args, "--input", name+"="+opts.Inputs[name])
	}
	for _, instruction := range opts.Instructions {
		if instruction != "" {
			args = append(args, "--instruction", instruction)
		}
	}
	if opts.Image != "" {
		args = append(args, "--image", opts.Image)
	}
	if opts.WorkspaceDiskSize != "" {
		args = append(args, "--workspace-disk-size", opts.WorkspaceDiskSize)
	}
	if opts.Engine != "" {
		args = append(args, "--engine", opts.Engine)
	}
	return append(args, "--disclose="+strconv.FormatBool(opts.Disclose))
}

// taskOutputBanner opens the task output in a run's Output, and
// bannerCloser closes it.
const taskOutputBanner = "================== TASK OUTPUT ================="

// bannerCloser closes what a banner opens.
const bannerCloser = "================================================"

// harvest reads a finished run's (or revise's) task output back by its run
// name and applies action to it, if any. Every action the runner applies
// is idempotent in factory (open-pr aliases to the branch's open PR, a
// reply carries its task's marker, post-review replaces the pending review
// factory posted before), so running it again after a restart repeats
// nothing. The namespace rides along because a Change names no sandbox:
// open-pr finds the issue's in it, to alias to the PR.
func (r *Runner) harvest(sandbox, namespace, runName, action, githubToken string) *preflight {
	return &preflight{
		harvest: func(ctx context.Context, out string, err error) (string, error) {
			if err != nil {
				return out, err
			}
			doc, err := r.execStdout(ctx, []string{"sandbox", "task", "output", sandbox, "--namespace", namespace, "--run-name", runName}, githubToken)
			if err != nil {
				return out + "\n" + doc, fmt.Errorf("reading the task output: %w", err)
			}
			section := taskOutputBanner + "\n" + doc + "\n" + bannerCloser + "\n"
			if action == "" {
				return section, nil
			}
			applied, err := r.applyDocIn(ctx, doc, action, namespace, githubToken)
			if err != nil {
				return section + applied, fmt.Errorf("applying %s: %w", action, err)
			}
			return section + applied, nil
		},
	}
}

// HarvestedTaskOutput is the task output in a run's result, whatever its
// kind, or "": the runner puts it after taskOutputBanner, and the closer
// after it.
func HarvestedTaskOutput(output string) string {
	start := strings.Index(output, taskOutputBanner)
	if start < 0 {
		return ""
	}
	rest := output[start+len(taskOutputBanner):]
	// A markdown spec may underline a heading with '='s: only the last
	// closer is the banner's.
	if end := strings.LastIndex(rest, bannerCloser); end >= 0 {
		rest = rest[:end]
	}
	rest = strings.TrimSpace(rest)
	if i := strings.Index(rest, "\napiVersion:"); !strings.HasPrefix(rest, "apiVersion:") && i >= 0 {
		rest = rest[i+1:]
	}
	if TaskOutputKind(rest) == "" {
		return ""
	}
	return rest + "\n"
}

// HarvestedOutput is the task output in a run's result when it is of kind
// and has a draft, or "".
func HarvestedOutput(kind, output string) string {
	doc := HarvestedTaskOutput(output)
	if TaskOutputKind(doc) != kind || Draft(kind, doc) == "" {
		return ""
	}
	return doc
}

// Recipes is the catalog of recipes this factory binary runs (factory
// recipe list -o json), in the board's order (OrderRecipes).
func (r *Runner) Recipes(ctx context.Context) ([]boardv1alpha1.BoardRecipe, error) {
	out, err := r.execStdout(ctx, []string{"recipe", "list", "-o", "json"}, "")
	if err != nil {
		return nil, fmt.Errorf("factory recipe list: %w: %s", err, tail(out, 2000))
	}
	var recipes []boardv1alpha1.BoardRecipe
	if err := json.Unmarshal([]byte(out), &recipes); err != nil {
		return nil, fmt.Errorf("reading factory recipe list: %w", err)
	}
	OrderRecipes(recipes)
	return recipes, nil
}

// boardOrder is where the built-ins go on a row, in the order the work on
// an issue goes; any other recipe follows them, by name.
var boardOrder = []string{"triage", "plan", "fix", "review", "research"}

// OrderRecipes sorts recipes in the board's order.
func OrderRecipes(recipes []boardv1alpha1.BoardRecipe) {
	rank := func(name string) int {
		if i := slices.Index(boardOrder, name); i >= 0 {
			return i
		}
		return len(boardOrder)
	}
	slices.SortStableFunc(recipes, func(a, b boardv1alpha1.BoardRecipe) int {
		return cmp.Or(cmp.Compare(rank(a.Name), rank(b.Name)), strings.Compare(a.Name, b.Name))
	})
}

// RecipeSandboxName is the sandbox factory runs recipe in on an issue or
// PR (item) of repo when there is none yet: an issue's is the issue's
// own, beside its triage, plan and fix (FixSandboxName); a PR's is the
// fix sandbox of the fix that opened it (PRFixSandbox), else one made for
// the PR, fix-<repo>-<pr>; a credentials: clone recipe's on a PR is one
// of its own named after it, as the review's is (factory's
// RecipeSandboxName and EnsurePRSandbox).
func RecipeSandboxName(recipe boardv1alpha1.BoardRecipe, item, repo string, number int) string {
	if item == "pr" && recipe.Credentials == "clone" {
		return fmt.Sprintf("%s-%s-%d", recipe.Name, repo, number)
	}
	return FixSandboxName(repo, number)
}
