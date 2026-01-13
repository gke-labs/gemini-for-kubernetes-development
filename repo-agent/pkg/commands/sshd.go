package commands

import (
	"context"
	"fmt"
	"os"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/sshd"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

func BuildSSHDCommand() *cobra.Command {
	return &cobra.Command{
		Use: "sshd",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("sshd command does not take any arguments")
			}
			return RunSSHD(cmd.Context())
		},
	}
}

func RunSSHD(ctx context.Context) error {
	log := klog.FromContext(ctx)

	conn := sshd.NewStdinStdoutConn(os.Stdin, os.Stdout)

	server := sshd.NewServer()

	if err := server.Start(ctx, conn); err != nil {
		log.Error(err, "SSH server exited with error")
		return fmt.Errorf("ssh server: %w", err)
	}

	// log.Info("SSH server exited successfully")
	return nil
}
