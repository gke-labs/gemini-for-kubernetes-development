package commands

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// GetGeminiAPIKey retrieves the Gemini API key from the environment variable.
// We also support executing a command to retrieve the key, if the value starts with "exec:".
func GetGeminiAPIKey(seed string) (string, error) {
	s := os.Getenv("GEMINI_API_KEY")
	if s == "" {
		return "", fmt.Errorf("GEMINI_API_KEY environment variable is not set")
	}
	if suffix, ok := strings.CutPrefix(s, "exec:"); ok {
		cmdParts := strings.Fields(suffix)
		if len(cmdParts) == 0 {
			return "", fmt.Errorf("invalid exec command for GEMINI_API_KEY: %q", s)
		}
		cmd := exec.Command(cmdParts[0], cmdParts[1:]...)

		var env []string
		for _, e := range os.Environ() {
			tokens := strings.SplitN(e, "=", 2)
			// Skip some variables that might interfere with the command
			if tokens[0] == "GEMINI_API_KEY" {
				continue
			}
			env = append(env, e)
		}
		env = append(env, fmt.Sprintf("SEED=%s", seed))
		cmd.Env = env

		out, err := cmd.Output()
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(out)), nil
	}
	return s, nil
}
