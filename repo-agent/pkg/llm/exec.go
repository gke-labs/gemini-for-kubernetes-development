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


package llm

import (
	"os/exec"
)

type CommandExecutor interface {
	Run(command string, args ...string) ([]byte, []byte, error)
}

// RealCommandExecutor is a real implementation of CommandExecutor that runs commands.

type RealCommandExecutor struct{}

func (e *RealCommandExecutor) Run(command string, args ...string) ([]byte, []byte, error) {
	const errBufferSize = 25 * 1024 * 1024 // 25MB
	const outBufferSize = 1024 * 1024      // 1MB
	stderrBuffer := NewCircularBuffer(errBufferSize)
	stdoutBuffer := NewCircularBuffer(outBufferSize)
	cmd := exec.Command(command, args...)
	// Dont return combined output. Return only stdout and log stderr separately.
	cmd.Stderr = stderrBuffer
	cmd.Stdout = stdoutBuffer
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
		return nil, stderrBuffer.Bytes(), err
	}

	return stdoutBuffer.Bytes(), stderrBuffer.Bytes(), nil
}
