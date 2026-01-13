package commands

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/codeserver"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

type CodeServerCommand struct {
	codeserverProcess *exec.Cmd
}

// BuildCodeServerCommand creates a new cobra command for running the code-server.
func BuildCodeServerCommand() *cobra.Command {
	cscmd := CodeServerCommand{}
	cmd := &cobra.Command{
		Use:   "codeserver",
		Short: "Run the code-server",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cscmd.Start(cmd.Context())
		},
	}

	return cmd
}

// Start runs the code-server and waits for it to exit.
func (c *CodeServerCommand) Start(ctx context.Context) error {
	var err error
	log := klog.FromContext(ctx)
	log.Info("Starting code-server command")

	c.codeserverProcess, err = codeserver.Start()
	if err != nil {
		return fmt.Errorf("failed to start code-server: %w", err)
	}

	return nil
}

func (c *CodeServerCommand) StopCodeServer(ctx context.Context) error {
	log := klog.FromContext(ctx)
	if c.codeserverProcess != nil && c.codeserverProcess.Process != nil {
		log.Info("Stopping code-server command")
		return c.codeserverProcess.Process.Kill()
	}
	return nil
}

func (c *CodeServerCommand) Wait() error {
	if c.codeserverProcess != nil && c.codeserverProcess.Process != nil {
		if err := c.codeserverProcess.Wait(); err != nil {
			return fmt.Errorf("code-server exited with error: %w", err)
		}
	}
	return nil
}
