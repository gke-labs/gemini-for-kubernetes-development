package commands

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/constants"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/recipe"
	factorysandbox "github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/sandbox"
	"github.com/spf13/cobra"
)

// slugifyScenario makes the scenario safe as a filename under
// runbooks/ (lowercase, dashes, nothing else). A typed ".md" is the
// name of the file, not part of it: "deploy-gcp.md" means the
// existing deploy-gcp run, never a new deploy-gcp-md twin.
func slugifyScenario(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimSuffix(s, ".md")
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:] // a pasted path names its file
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// NewRunCommand plans, deploys or tears down one run.
//
//	factory run plan     --url <repo> --name deploy-gke-k8s1 [--intent …] [--runbook …] [--target <PR>]
//	factory run deploy   --url <repo> --name deploy-gke-k8s1
//	factory run teardown --url <repo> --name deploy-gke-k8s1
//
// Plan does the drafting too. It writes the procedure first and the
// scripts from it, and where the parameters cannot be resolved — no
// GCP project configured, a target the owner has not chosen — it
// stops after the procedure and says what it needs. Nothing executes
// at plan time either way, so there was never a reason to offer a
// separate, earlier stopping point.
//
// A run owns everything it needs, in one directory on the fork's
// research/runs branch: runbook.md is the procedure, the scripts
// are generated from it, the receipts are the test results. Nothing is
// shared between runs, which is what lets a deploy correct the
// procedure in place instead of filing recommendations against a
// document other runs also read.
//
// A repository the member cannot fork runs local-only: the branch is
// committed in the run's sandbox and never pushed (lib.sh's
// enterLocalOnly).
//
// --runbook starts a new run from an existing runbook instead of from
// nothing: the repository's .agents/runbooks/<name>/ if it has one,
// else one of the member's own runs (any run is a runbook). It is
// copied into the new run's directory as is, and the plan makes the
// copy this run's — its prefix, project and region, plus whatever
// --intent asks to change — and verifies it here. It replaces --from.
//
// --target deploys a pull request instead of the default branch. The
// plan pins the pull request's head commit in the run's directory, and
// deploy and teardown execute exactly that commit — what runs is what
// was reviewed. A re-plan with --target moves the pin to the current
// head. The resource prefix is still the run's: a run for a pull
// request is a different run from the default branch's, with its own
// name, so nothing it creates can collide with that deployment.
//
// This supersedes `factory runbook`, where a runbook was a separate
// shared document and an instance only held the artifacts. That split
// is what sibling-hiding, the read-only guard and the stash-restore
// dance existed to police; none of them are needed here.
func NewRunCommand(ctx context.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Plan, deploy or tear down a run",
	}

	var repoURL, name, intent, runbook, from, target string

	exec := func(mode string) func(*cobra.Command, []string) error {
		return func(c *cobra.Command, _ []string) error {
			if _, err := ResolveRootFlags(c); err != nil {
				return err
			}
			if repoURL == "" {
				return fmt.Errorf("--url is required")
			}
			name = slugifyScenario(name)
			if name == "" {
				return fmt.Errorf("--name is required (the run's identity, e.g. deploy-gke-k8s1)")
			}
			if runbook == "" {
				runbook = from // deprecated spelling
			}
			runbook = slugifyScenario(runbook)
			if runbook != "" && mode != "plan" {
				return fmt.Errorf("--runbook applies to plan only")
			}
			if runbook == name {
				return fmt.Errorf("--runbook %s names this run itself; a run is started from a different one", runbook)
			}
			if target != "" && mode != "plan" {
				return fmt.Errorf("--target applies to plan only; deploy and teardown execute the commit the plan pinned")
			}
			// Runs build images; the 6Gi ephemeral default is sized for
			// code tasks. 10Gi is the GKE Autopilot per-pod ceiling —
			// only when nothing chose a value explicitly.
			if ephemeralStorageDefaulted {
				rootFlags.EphemeralStorage = "10Gi"
			}
			// Go build+module caches for a big monorepo alone run
			// ~7.5Gi on the PVC; 10Gi leaves no room for the repo,
			// images and artifacts.
			if workspaceDiskDefaulted {
				rootFlags.DiskSize = "30Gi"
			}
			if rootFlags.Timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, rootFlags.Timeout)
				defer cancel()
			}
			return runRun(ctx, mode, repoURL, name, intent, runbook, target)
		}
	}

	planCmd := &cobra.Command{
		Use:   "plan",
		Short: "Author the procedure and generate its scripts; nothing executes",
		RunE:  exec("plan"),
	}
	deployCmd := &cobra.Command{
		Use:   "deploy",
		Short: "Execute an approved plan, then reconcile runbook.md to what actually worked",
		RunE:  exec("deploy"),
	}
	teardownCmd := &cobra.Command{
		Use:   "teardown",
		Short: "Remove what this run created (prefers the generated teardown script)",
		RunE:  exec("teardown"),
	}

	for _, sub := range []*cobra.Command{planCmd, deployCmd, teardownCmd} {
		sub.Flags().StringVar(&repoURL, "url", "", "GitHub repository URL (e.g. https://github.com/owner/repo)")
		sub.Flags().StringVar(&name, "name", "", "The run's identity and directory (e.g. deploy-gke-k8s1)")
		cmd.AddCommand(sub)
	}
	planCmd.Flags().StringVar(&intent, "intent", "", "What this run should do, or what to change on a re-plan")
	planCmd.Flags().StringVar(&runbook, "runbook", "", "Start from an existing runbook: .agents/runbooks/<name> in the repo, else one of your runs")
	planCmd.Flags().StringVar(&from, "from", "", "Deprecated: use --runbook")
	_ = planCmd.Flags().MarkDeprecated("from", "use --runbook")
	planCmd.Flags().StringVar(&target, "target", "", "Deploy a pull request instead of the default branch: its number or URL. The plan pins its head commit")
	teardownCmd.Flags().StringVar(&intent, "intent", "", "Anything the owner wants watched during teardown")

	return cmd
}

func runRun(ctx context.Context, mode, repoURL, name, intent, runbook, target string) error {
	u, err := url.Parse(repoURL)
	if err != nil {
		return fmt.Errorf("invalid repo URL: %w", err)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 {
		return fmt.Errorf("expected URL format https://github.com/owner/repo, got %s", repoURL)
	}
	owner, repo := parts[0], strings.TrimSuffix(parts[1], ".git")
	pr, err := parseTargetPR(target, owner, repo)
	if err != nil {
		return err
	}
	htmlURL := fmt.Sprintf("https://github.com/%s/%s", owner, repo)

	kubeClient, err := clients.NewKubernetesClient()
	if err != nil {
		return fmt.Errorf("creating k8s client: %w", err)
	}
	// BYO GCP project: Workload Identity supplies credentials via the
	// pod's KSA; the secret only carries where to deploy.
	secret, err := kubeClient.Clientset.CoreV1().Secrets(rootFlags.Namespace).Get(ctx, rootFlags.SecretName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("fetching %s secret in namespace %s: %w (make sure to run 'factory user onboard' first)", rootFlags.SecretName, rootFlags.Namespace, err)
	}

	inputs := map[string]string{
		"run":             name,
		"resource_prefix": resourcePrefix(repo, name),
		"gcp_project":     string(secret.Data[constants.KeyGcpProject]),
		"gcp_region":      string(secret.Data[constants.KeyGcpRegion]),
	}
	switch mode {
	case "plan":
		inputs["intent"], inputs["runbook"] = intent, runbook
		if pr > 0 {
			inputs["target_pr"] = strconv.Itoa(pr)
		}
	case "teardown":
		inputs["intent"] = intent
	}
	// Sandbox naming is unchanged from the runbook command on purpose:
	// live deployments hold their teardown scripts inside these
	// sandboxes, and renaming would orphan them.
	ensure := func(ctx context.Context, kubeClient *clients.KubernetesClient, cloneURL, htmlURL, user string) (string, error) {
		fmt.Printf("Ensuring run sandbox for %s/%s (run %s)...\n", owner, repo, name)
		return factorysandbox.EnsureRunbookSandbox(ctx, kubeClient, rootFlags.Namespace, repo, name, name, cloneURL, htmlURL, rootFlags.Image, rootFlags.DiskSize, rootFlags.StorageClass, rootFlags.EphemeralStorage, rootFlags.ResolvedSecrets, rootFlags.ResolvedEnvs, user)
	}
	if err := runRecipeIn(ctx, recipeRunOpts{ensure: ensure}, recipe.RunRecipe(mode), htmlURL, "", "", "", false, applyMode{}, inputs, nil); err != nil {
		return err
	}
	if !rootFlags.Detached {
		fmt.Printf("Run %s %s finished for %s/%s; recorded on the research/runs branch.\n", name, mode, owner, repo)
	}
	return nil
}

// parseTargetPR reads --target: a pull request number ("42", "#42") or
// its URL. A URL must name the run's own repository — the sandbox has
// that repository checked out, and a pull request number means nothing
// in any other. Empty is the default branch, 0.
func parseTargetPR(target, owner, repo string) (int, error) {
	t := strings.TrimSpace(target)
	if t == "" {
		return 0, nil
	}
	if strings.Contains(t, "/") {
		u, err := url.Parse(t)
		if err != nil {
			return 0, fmt.Errorf("--target %q: %w", target, err)
		}
		p := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(p) != 4 || p[2] != "pull" {
			return 0, fmt.Errorf("--target %q is not a pull request URL (https://github.com/owner/repo/pull/N)", target)
		}
		if !strings.EqualFold(p[0], owner) || !strings.EqualFold(p[1], repo) {
			return 0, fmt.Errorf("--target %q is a pull request in %s/%s, not in %s/%s", target, p[0], p[1], owner, repo)
		}
		t = p[3]
	}
	n, err := strconv.Atoi(strings.TrimPrefix(t, "#"))
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("--target %q is not a pull request number or URL", target)
	}
	return n, nil
}

// shortName compresses a repo name for resource prefixes: initials of
// hyphenated words (in-cluster-storage → ics), else the name truncated.
func shortName(repo string) string {
	slug := slugifyScenario(repo)
	words := strings.Split(slug, "-")
	if len(words) >= 2 {
		var b strings.Builder
		for _, w := range words {
			if w != "" {
				b.WriteByte(w[0])
			}
		}
		return b.String()
	}
	if len(slug) > 8 {
		return slug[:8]
	}
	return slug
}

// resourcePrefix is the project-unique identity for everything a run
// creates: shortName(repo)-name. Deliberately dumb — no stutter
// collapsing (it would be renaming the user cannot control, and it can
// alias two runs onto one prefix). Stutter is prevented at the source:
// the UI shows the prefix so nobody hand-types it.
func resourcePrefix(repo, name string) string {
	return shortName(repo) + "-" + name
}
