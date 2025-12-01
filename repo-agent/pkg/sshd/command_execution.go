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
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/creack/pty"
	"golang.org/x/crypto/ssh"
	"k8s.io/klog/v2"
)

func (s *Server) startCommand(ctx context.Context, sshChannel ssh.Channel, command string, ptyOptions *sshPayload) error {
	log := klog.FromContext(ctx)

	defer sshChannel.Close()

	userHomeDir := os.Getenv("HOME")

	cmd := exec.CommandContext(ctx, command)
	cmd.Dir = userHomeDir
	env := []string{}
	env = append(env, os.Environ()...)
	// env = append(env, "TERM=xterm-256color")
	cmd.Env = env

	inputPipeResults := make(chan error, 2)

	if ptyOptions != nil {
		ptmx, err := pty.Start(cmd)
		if err != nil {
			return err
		}
		defer func() {
			if err := ptmx.Close(); err != nil {
				if !errors.Is(err, os.ErrClosed) {
					log.Error(err, "closing pty")
				}
			}
		}()

		go func() {
			// defer ptmx.Close()
			_, err := io.Copy(ptmx, sshChannel)
			if err != nil && !errors.Is(err, io.EOF) {
				log.Error(err, "copying ssh channel to pty")
			}
		}()

		go func() {
			// defer sshChannel.CloseWrite()
			_, err := io.Copy(sshChannel, ptmx)
			inputPipeResults <- err
			if err != nil && !errors.Is(err, io.EOF) {
				log.V(2).Info("copying pty to ssh channel", "error", err)
			}
		}()

		// We only have one channel when using a pty
		inputPipeResults <- nil
	} else {
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return fmt.Errorf("getting stdin pipe: %w", err)
		}

		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return fmt.Errorf("getting stdout pipe: %w", err)
		}
		stderr, err := cmd.StderrPipe()
		if err != nil {
			return fmt.Errorf("getting stderr pipe: %w", err)
		}

		if err := cmd.Start(); err != nil {
			return fmt.Errorf("starting exec command: %w", err)
		}

		go func() {
			defer stdin.Close()
			_, err := io.Copy(stdin, sshChannel)
			if err != nil && !errors.Is(err, io.EOF) {
				log.Error(err, "copying ssh channel to stdin")
			}
		}()

		go func() {
			// defer sshChannel.CloseWrite()
			_, err := io.Copy(sshChannel, stdout)
			inputPipeResults <- err
			if err != nil && !errors.Is(err, io.EOF) {
				log.Error(err, "copying stdout to ssh channel")
			}
		}()

		go func() {
			// defer sshChannel.CloseWrite()
			_, err := io.Copy(sshChannel.Stderr(), stderr)
			inputPipeResults <- err
			if err != nil && !errors.Is(err, io.EOF) {
				log.Error(err, "copying stderr to ssh channel")
			}
		}()
	}

	exitStatus := 0
	if err := cmd.Wait(); err != nil {
		log.Error(err, "command exited with error")
		if exitErr, ok := err.(*exec.ExitError); ok {
			if status, ok := exitErr.Sys().(interface{ ExitStatus() int }); ok {
				exitStatus = status.ExitStatus()
			}
		} else {
			log.Error(err, "unexpected error waiting for command")
			exitStatus = 255
		}
	}

	exitStatusBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(exitStatusBytes, uint32(exitStatus))
	if _, err := sshChannel.SendRequest("exit-status", false, exitStatusBytes); err != nil {
		log.Error(err, "sending exit-status message")
	}

	// Wait for pipes to finish before sending exit code
	err1 := <-inputPipeResults
	if errors.Is(err1, io.EOF) {
		err1 = nil
	}
	err2 := <-inputPipeResults
	if errors.Is(err2, io.EOF) {
		err2 = nil
	}
	if err := errors.Join(err1, err2); err != nil {
		log.V(2).Info("error copying between pipes", "error", err)
	}

	return nil
}
