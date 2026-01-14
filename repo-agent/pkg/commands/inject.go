package commands

import (
	"fmt"
	"io"
	"os"
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

	fmt.Printf("Successfully injected %s to %s\n", src, dest)
	return nil
}
