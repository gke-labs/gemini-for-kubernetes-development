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

package llm

import (
	"bytes"
	"os/exec"
)

type CommandExecutor interface {
	Run(command string, args ...string) ([]byte, error)
}

// RealCommandExecutor is a real implementation of CommandExecutor that runs commands.

type RealCommandExecutor struct{}

func (e *RealCommandExecutor) Run(command string, args ...string) ([]byte, error) {
	const bufferSize = 25 * 1024 * 1024 // 25MB
	stderrBuffer := NewCircularBuffer(bufferSize)
	cmd := exec.Command(command, args...)
	// Dont return combined output. Return only stdout and log stderr separately.
	// Create a buffer to capture stdout
	var stdout bytes.Buffer
	cmd.Stderr = stderrBuffer
	cmd.Stdout = &stdout
	err := cmd.Run()
	// Retrieve the captured output from the buffer
	capturedOutput := stderrBuffer.String()
	if len(capturedOutput) > 0 {
		// Log the stderr output
		// In real implementation, use a proper logging framework
		println("Captured Stderr Output (truncated to 25MB):")
		println(capturedOutput)
	}
	if err != nil {
		return nil, err
	}

	return stdout.Bytes(), nil
}
