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

	// Inject CA cert if found
	caSrc := "/etc/github-portal/ca/tls.crt"
	if _, err := os.Stat(caSrc); err == nil {
		caDestDir := filepath.Join(destDir, "ca")
		if err := os.MkdirAll(caDestDir, 0755); err != nil {
			return fmt.Errorf("failed to create CA destination directory: %w", err)
		}
		caDest := filepath.Join(caDestDir, "tls.crt")
		if err := copyFile(caSrc, caDest); err != nil {
			return err
		}
		fmt.Printf("Successfully injected %s to %s\n", caSrc, caDest)
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
