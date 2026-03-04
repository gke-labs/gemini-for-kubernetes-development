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

	var envVars []string

	var ptySize sshWindowSize
	var pty *PTY

	for request := range requests {
		sshPayload := sshPayload{b: request.Payload}

		ackMessage := func(err error) {
			success := true
			if err != nil {
				success = false
				log.Error(err, "handling channel request", "type", request.Type)
			}
			if request.WantReply {
				if err := request.Reply(success, nil); err != nil {
					log.Error(err, "replying to request")
				}
			}
		}

		// log.Info("Received channel request", "type", request.Type)
		switch request.Type {
		case "pty-req":
			// The payload format:
			// string: TERM environment variable value (e.g., xterm-256color)
			// uint32: terminal width, columns
			// uint32: terminal height, rows
			// uint32: terminal width, pixels
			// uint32: terminal height, pixels
			// string: terminal modes (we ignore this)

			term, err := sshPayload.PopString()
			if err != nil {
				ackMessage(err)
				continue
			}

			if err := sshPayload.Unmarshal(16, &ptySize); err != nil {
				ackMessage(err)
				continue
			}

			log.V(4).Info("PTY requested", "term", term, "ptySize", ptySize)
			envVars = append(envVars, fmt.Sprintf("TERM=%s", term))

			pty, err = NewPTY()
			if err != nil {
				ackMessage(err)
				return err
			}

			if err := pty.SetSize(&ptySize); err != nil {
				ackMessage(err)
				return err
			}

			ackMessage(nil)

		case "window-change":
			if err := sshPayload.Unmarshal(16, &ptySize); err != nil {
				ackMessage(err)
				continue
			}

			log.V(4).Info("window-change", "ptySize", ptySize)

			// Update the active PTY
			if pty != nil {
				if err := pty.SetSize(&ptySize); err != nil {
					ackMessage(err)
					continue
				}
			}

			ackMessage(nil)

		case "env":
			name, err := sshPayload.PopString()
			if err != nil {
				ackMessage(err)
				continue
			}
			value, err := sshPayload.PopString()
			if err != nil {
				ackMessage(err)
				if err := request.Reply(false, nil); err != nil {
					return fmt.Errorf("replying to env request: %w", err)
				}
				continue
			}
			envVars = append(envVars, fmt.Sprintf("%s=%s", name, value))
			log.V(4).Info("Environment variable set", "name", name, "value", value)
			ackMessage(nil)

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
			if err := s.startCommand(ctx, sshChannel, shellCommand, pty, envVars); err != nil {
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

			if err := s.startCommand(ctx, sshChannel, command, pty, envVars); err != nil {
				return fmt.Errorf("running exec: %w", err)
			}

		default:
			log.Info("Received unknown channel request", "type", request.Type)
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
