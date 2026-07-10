package commands

import (
	"context"
	"fmt"
	"os"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/sshd"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

func NewSSHDCommand(ctx context.Context) *cobra.Command {
	return &cobra.Command{
		Use:   "sshd",
		Short: "Start an embedded SSH server over Stdin/Stdout for terminal forwarding",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
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

	return nil
}
