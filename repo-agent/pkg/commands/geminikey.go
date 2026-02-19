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
		// read from /tokens/gemini file if it exists
		data, err := os.ReadFile("/tokens/gemini")
		if err == nil {
			s = strings.TrimSpace(string(data))
		}
	}
	if s == "" {
		return "", fmt.Errorf("GEMINI_API_KEY environment variable is not set")
	}

	var rawKey string
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
		rawKey = strings.TrimSpace(string(out))
	} else {
		rawKey = s
	}

	// Normalize multiple keys (comma, space, or newline separated) to comma separated
	keys := strings.FieldsFunc(rawKey, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\n' || r == '\r' || r == '\t'
	})

	return strings.Join(keys, ","), nil
}
