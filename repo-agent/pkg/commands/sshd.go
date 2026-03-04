// Copyright 2026 The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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
