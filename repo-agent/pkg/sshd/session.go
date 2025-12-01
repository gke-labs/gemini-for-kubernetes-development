// Copyright 2025 Google LLC
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

package sshd

import (
	"context"
	"fmt"

	"golang.org/x/crypto/ssh"
	"k8s.io/klog/v2"
)

func (s *Server) runSessionChannel(ctx context.Context, sshChannel ssh.Channel, requests <-chan *ssh.Request) error {
	log := klog.FromContext(ctx)

	defer sshChannel.Close()

	var pty *sshPayload

	for request := range requests {
		sshPayload := sshPayload{b: request.Payload}
		// log.Info("Received channel request", "type", request.Type)
		switch request.Type {
		case "pty-req":
			// golang.org/x/crypto/ssh magically parses the PTY request payload for us
			if request.WantReply {
				if err := request.Reply(true, nil); err != nil {
					return fmt.Errorf("replying to pty-req: %w", err)
				}
			}
			pty = &sshPayload

			// log.Info("PTY request accepted", "payload", string(sshPayload.b))
		case "shell":
			if sshPayload.Len() != 0 {
				log.Error(fmt.Errorf("non-empty payload for shell request"), "invalid shell request payload")
				if err := request.Reply(false, nil); err != nil {
					return fmt.Errorf("replying to shell request: %w", err)
				}
				continue
			}

			if request.WantReply {
				if err := request.Reply(true, nil); err != nil {
					return fmt.Errorf("replying to shell request: %w", err)
				}
			}

			shellCommand := "/bin/bash"
			if err := s.startCommand(ctx, sshChannel, shellCommand, pty); err != nil {
				return fmt.Errorf("running shell: %w", err)
			}

		case "exec":
			command, err := sshPayload.PopString()
			if err != nil {
				log.Error(err, "parsing exec payload")
				if err := request.Reply(false, nil); err != nil {
					return fmt.Errorf("replying to exec request: %w", err)
				}
				continue
			}

			if request.WantReply {
				if err := request.Reply(true, nil); err != nil {
					return fmt.Errorf("replying to exec request: %w", err)
				}
			}

			if err := s.startCommand(ctx, sshChannel, command, pty); err != nil {
				return fmt.Errorf("running exec: %w", err)
			}

		default:
			if request.WantReply {
				if err := request.Reply(false, nil); err != nil {
					return fmt.Errorf("replying to unknown channel request %q: %w", request.Type, err)
				}
			}
			return fmt.Errorf("unknown channel request type: %s", request.Type)
		}
	}
	return nil
}
