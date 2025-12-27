package commands

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
	"sigs.k8s.io/yaml"
)

// CreateOptions holds options for the Create command.
type CreateOptions struct {
	Name      string
	Repo      string
	Branch    string
	Dotfiles  string
	Namespace string
}

// InitDefaults initializes default values for CreateOptions.
func (o *CreateOptions) InitDefaults() {
	// No defaults to set currently
}

// BuildCreateCommand builds the cobra command for creating a dev sandbox.
func BuildCreateCommand() *cobra.Command {
	var opt CreateOptions
	opt.InitDefaults()

	cmd := &cobra.Command{
		Use:   "create [NAME]",
		Short: "Create a new dev sandbox",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opt.Name = args[0]
			return RunCreate(cmd.Context(), opt)
		},
	}
	cmd.Flags().StringVar(&opt.Repo, "repo", "", "URL of the repository")
	cmd.Flags().StringVar(&opt.Branch, "branch", "", "Branch to checkout")
	cmd.Flags().StringVar(&opt.Dotfiles, "dotfiles", "", "URL of the dotfiles repository")
	cmd.Flags().StringVar(&opt.Namespace, "namespace", "default", "Namespace to create the sandbox in")
	_ = cmd.MarkFlagRequired("repo")

	return cmd
}

// RunCreate executes the create logic.
func RunCreate(ctx context.Context, opt CreateOptions) error {
	htmlURL := strings.TrimSuffix(opt.Repo, ".git")
	cloneURL := htmlURL + ".git"
	if opt.Branch != "" {
		cloneURL += "#refs/heads/" + opt.Branch
	}

	sandbox := DevSandbox{
		APIVersion: "custom.agents.x-k8s.io/v1alpha1",
		Kind:       "DevSandbox",
		Metadata: DevSandboxMeta{
			Name:      opt.Name,
			Namespace: opt.Namespace,
		},
		Spec: DevSandboxSpec{
			Source: DevSandboxSource{
				CloneURL: cloneURL,
				HTMLURL:  htmlURL,
			},
			ServiceAccountName: "issue-sandbox",
			Destination: DevSandboxDest{
				Branch: "master",
			},
		},
	}

	if opt.Branch != "" {
		sandbox.Spec.Destination.Branch = opt.Branch
	}
	if opt.Dotfiles != "" {
		sandbox.Spec.User.DotFilesRepo = opt.Dotfiles
	}

	data, err := yaml.Marshal(sandbox)
	if err != nil {
		return fmt.Errorf("marshalling yaml: %w", err)
	}

	kubectlCmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", "-")
	kubectlCmd.Stdin = strings.NewReader(string(data))
	kubectlCmd.Stdout = os.Stdout
	kubectlCmd.Stderr = os.Stderr

	if err := kubectlCmd.Run(); err != nil {
		return fmt.Errorf("running kubectl: %w", err)
	}

	fmt.Printf("DevSandbox %q created\n", opt.Name)
	return nil
}

type DevSandbox struct {
	APIVersion string         `json:"apiVersion"`
	Kind       string         `json:"kind"`
	Metadata   DevSandboxMeta `json:"metadata"`
	Spec       DevSandboxSpec `json:"spec"`
}

type DevSandboxMeta struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}

type DevSandboxSpec struct {
	User               DevSandboxUser   `json:"user,omitempty"`
	ServiceAccountName string           `json:"serviceAccountName,omitempty"`
	Source             DevSandboxSource `json:"source,omitempty"`
	Destination        DevSandboxDest   `json:"destination,omitempty"`
}

type DevSandboxUser struct {
	DotFilesRepo string `json:"dotFilesRepo,omitempty"`
}

type DevSandboxSource struct {
	CloneURL string `json:"cloneURL,omitempty"`
	HTMLURL  string `json:"htmlURL,omitempty"`
}

type DevSandboxDest struct {
	Branch string `json:"branch,omitempty"`
}
