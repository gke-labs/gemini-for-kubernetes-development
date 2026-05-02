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
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"
)

func BuildInjectCommand() *cobra.Command {
	var path string
	cmd := &cobra.Command{
		Use:   "inject",
		Short: "Copy the binary to a specific path",
		RunE: func(_ *cobra.Command, _ []string) error {
			if path == "" {
				return fmt.Errorf("path is required")
			}
			return inject(path)
		},
	}
	cmd.Flags().StringVar(&path, "path", "", "Path to copy the binary to")
	return cmd
}

func inject(destDir string) error {
	src, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get current executable path: %w", err)
	}

	// Ensure destination directory exists
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("failed to create destination directory: %w", err)
	}

	dest := filepath.Join(destDir, filepath.Base(src))

	if err := copyFile(src, dest); err != nil {
		return err
	}
	fmt.Printf("Successfully injected %s to %s\n", src, dest)

	// Inject gemini-stream-processor if found
	gspSrc, err := exec.LookPath("gemini-stream-processor")
	if err != nil {
		// Fallback to checking the exact path in the docker image
		if _, err := os.Stat("/usr/local/bin/gemini-stream-processor"); err == nil {
			gspSrc = "/usr/local/bin/gemini-stream-processor"
		}
	}
	if gspSrc != "" {
		gspDest := filepath.Join(destDir, "gemini-stream-processor")
		if err := copyFile(gspSrc, gspDest); err != nil {
			return err
		}
		fmt.Printf("Successfully injected %s to %s\n", gspSrc, gspDest)
	}

	return nil
}

func copyFile(src, dest string) error {
	sourceFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("failed to open source file: %w", err)
	}
	defer sourceFile.Close()

	destFile, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("failed to create destination file: %w", err)
	}
	defer destFile.Close()

	if _, err := io.Copy(destFile, sourceFile); err != nil {
		return fmt.Errorf("failed to copy file: %w", err)
	}

	if err := os.Chmod(dest, 0755); err != nil {
		return fmt.Errorf("failed to chmod destination file: %w", err)
	}

	return nil
}
