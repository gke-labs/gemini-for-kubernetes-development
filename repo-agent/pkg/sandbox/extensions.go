package sandbox

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

var ExecCommand = exec.Command

// InstallExtensions reads a file named extensions.txt and installs the extensions listed in it.
// Each line in the file should be in the format: <extension_source> [version]
func InstallExtensions() error {
	file, err := os.Open("extensions.txt")
	if err != nil {
		if os.IsNotExist(err) {
			// If the file doesn't exist, there's nothing to do.
			return nil
		}
		return fmt.Errorf("failed to open extensions.txt: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		parts := strings.Fields(line)
		source := parts[0]
		version := ""
		if len(parts) > 1 {
			version = parts[1]
		}

		cmdArgs := []string{"extensions", "install", "--auto-update", "--consent", source}
		if version != "" {
			cmdArgs = append(cmdArgs, version)
		}

		cmd := ExecCommand("gemini", cmdArgs...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to install extension %q: %w", source, err)
		}
.Close()
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("failed to read extensions.txt: %w", err)
	}

	return nil
}
