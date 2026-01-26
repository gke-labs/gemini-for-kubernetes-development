package commands

import (
	"context"
	"fmt"
	"os"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/github"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/prompts"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/sandbox"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

// GithubFeedbackOptions holds options for the RunCode function.
type GithubFeedbackOptions struct {
	Repo        string
	PullRequest int
	Sandbox     string
}

// BuildGithubFeedbackCommand creates a new cobra command for using a dev sandbox to address github feedback
func BuildGithubFeedbackCommand() *cobra.Command {
	var opt GithubFeedbackOptions

	cmd := &cobra.Command{
		Use:   "github-feedback",
		Short: "Address github pull request feedback using an LLM in a dev sandbox",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("command does not take positional arguments")
			}

			return RunGithubFeedback(cmd.Context(), opt)
		},
	}

	cmd.Flags().StringVar(&opt.Sandbox, "sandbox", opt.Sandbox, "Name of existing sandbox to reuse")
	cmd.Flags().StringVar(&opt.Repo, "repo", opt.Repo, "GitHub repository (e.g., gke-labs/gemini-for-kubernetes-development)")
	cmd.Flags().IntVar(&opt.PullRequest, "pull-request", opt.PullRequest, "GitHub pull request number")
	return cmd
}

// RunGithubFeedback launches gemini-cli to respond to the specified GitHub pull request feedback.
func RunGithubFeedback(ctx context.Context, opt GithubFeedbackOptions) error {
	log := klog.FromContext(ctx)

	githubAPI, err := github.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("failed to create github client: %w", err)
	}

	kube, err := clients.NewKubernetesClient()
	if err != nil {
		return err
	}

	repo, err := github.ParseRepo(opt.Repo)
	if err != nil {
		return err
	}

	if opt.PullRequest == 0 {
		return fmt.Errorf("--pull-request is required")
	}
	if opt.Repo == "" {
		return fmt.Errorf("--repo is required")
	}
	if opt.Sandbox == "" {
		// TODO: We could choose instead to launch a sandbox here
		return fmt.Errorf("--sandbox is required")
	}

	prompt, err := prompts.FixPRFeedbackPrompt(ctx, githubAPI, repo, opt.PullRequest)
	if err != nil {
		return fmt.Errorf("failed to generate prompt for pull-request: %w", err)
	}

	podID, err := sandbox.FindSandboxPod(ctx, opt.Sandbox)
	if err != nil {
		return err
	}
	if podID == nil {
		return fmt.Errorf("sandbox %q not found", opt.Sandbox)
	}

	geminiAPIKey, err := GetGeminiAPIKey(podID.Namespace + "/" + podID.Name)
	if err != nil {
		return err
	}

	// Copy the prompt into the pod (for now)
	if len(prompt) > 0 {
		path := "/workspaces/prompt.txt"
		if err := sandbox.WriteFileInPod(ctx, kube, *podID, path, prompt); err != nil {
			return fmt.Errorf("copying prompt into sandbox pod: %w", err)
		}

		log.Info("wrote prompt into sandbox pod", "pod", podID.Name, "path", path)
	}

	workdir := fmt.Sprintf("/workspaces/%s", repo.FilesystemName())

	// Run gemini with API key and prompt
	{
		log.Info("Running gemini in pod", "pod", podID.Name)

		// TODO:
		// export GEMINI_TELEMETRY_ENABLED=true
		// export GEMINI_TELEMETRY_OTLP_ENDPOINT=http://otel-portal.otel-system:4317

		opts := sandbox.ExecOptions{
			Command: []string{"sh", "-c", fmt.Sprintf("cd %s && export GEMINI_API_KEY=%s && gemini --yolo --model gemini-3-pro-preview < /workspaces/prompt.txt", workdir, geminiAPIKey)},
			Stdout:  os.Stdout,
			Stderr:  os.Stderr,
		}
		opts.Secrets = []string{geminiAPIKey}

		if err := sandbox.ExecInPod(ctx, kube, *podID, opts); err != nil {
			return fmt.Errorf("running gemini in pod: %w", err)
		}
	}

	return nil
}
