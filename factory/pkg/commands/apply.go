package commands

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/taskoutput"
)

// NewApplyCommand acts on task outputs: what a task found, applied to
// GitHub by whoever runs it, with their token. Tasks never write their
// results themselves, so a result can be looked at, edited, or dropped
// before anything is written.
func NewApplyCommand(ctx context.Context) *cobra.Command {
	var file string
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "apply -f <file | ->",
		Short: "Apply task outputs (a triage …) to GitHub",
		Long: `Apply task outputs to GitHub.

A task output is the result a task leaves in its task directory
(task-output.yaml): its kind, the issue or PR it is about, and the result.
` + "`factory sandbox task output`" + ` prints it. Applying it writes it to the
issue or PR with your GitHub credentials (GITHUB_TOKEN, else gh's).

Kinds:
  Triage  adds the labels and comments the assessment on the issue

Applying the same task's output again does not comment again.`,
		Example: `  factory sandbox task output recipe-repo-123 | factory apply -f - --dry-run
  factory sandbox task output recipe-repo-123 > triage.yaml   # look, edit
  factory apply -f triage.yaml`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			var data []byte
			var err error
			if file == "-" {
				data, err = io.ReadAll(os.Stdin)
			} else {
				data, err = os.ReadFile(file)
			}
			if err != nil {
				return fmt.Errorf("reading %s: %w", file, err)
			}
			docs, err := taskoutput.Parse(data)
			if err != nil {
				return err
			}
			gh, err := github.NewClient(ctx)
			if err != nil {
				return fmt.Errorf("creating github client: %w", err)
			}
			for _, d := range docs {
				if err := taskoutput.Apply(ctx, gh, d, dryRun, os.Stdout); err != nil {
					return fmt.Errorf("applying the %s for %s: %w", d.Kind, d.Target.URL, err)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&file, "filename", "f", "", "Task output file, or - for stdin")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print what would be written, and write nothing")
	_ = cmd.MarkFlagRequired("filename")
	return cmd
}
