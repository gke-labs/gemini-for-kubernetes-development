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
	"errors"
	"os/exec"
	"testing"
)

func TestRealCommandExecutor_Run(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		// This test will fail if `echo` is not in the path
		executor := &RealCommandExecutor{}
		output, err := executor.Run("echo", "-n", "hello")
		if err != nil {
			t.Fatalf("RealCommandExecutor.Run() failed: %v", err)
		}
		if string(output) != "hello" {
			t.Errorf("Expected output %q, but got %q", "hello", string(output))
		}
	})

	t.Run("error", func(t *testing.T) {
		// This test will fail if `command-that-does-not-exist` is in the path
		executor := &RealCommandExecutor{}
		_, err := executor.Run("command-that-does-not-exist")
		if err == nil {
			t.Fatal("RealCommandExecutor.Run() should have failed, but it didn't")
		}

		// Check if the error is of type *exec.Error
		var execErr *exec.Error
		if !errors.As(err, &execErr) {
			t.Errorf("Expected error of type *exec.Error, but got %T", err)
		}
	})

	t.Run("stderr", func(t *testing.T) {
		// This test will fail if `sh` is not in the path
		executor := &RealCommandExecutor{}
		_, err := executor.Run("sh", "-c", "echo -n hello >&2")
		if err != nil {
			t.Fatalf("RealCommandExecutor.Run() failed: %v", err)
		}
	})
}
