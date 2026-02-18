package tokens

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// GetGitHubToken returns the effective GitHub token from environment variables.
// It follows the hierarchy: MANUAL_PAT > OAUTH_PAT > GITHUB_TOKEN.
func GetGitHubToken() string {
	if token := os.Getenv("MANUAL_PAT"); token != "" {
		return token
	}
	if token := os.Getenv("OAUTH_PAT"); token != "" {
		return token
	}
	return os.Getenv("GITHUB_TOKEN")
}

// GetGeminiAPIKey retrieves a single Gemini API key (picked from a list if necessary).
func GetGeminiAPIKey(seed string) (string, error) {
	s, err := GetRawGeminiAPIKey(seed)
	if err != nil {
		return "", err
	}
	return PickKey(s, seed), nil
}

// GetRawGeminiAPIKey retrieves the raw Gemini API key(s) from the environment or file.
// It may return multiple keys (newline separated) or an "exec:..." command.
func GetRawGeminiAPIKey(seed string) (string, error) {
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
		return string(out), nil
	}
	return s, nil
}

// PickKey selects one key from a newline-separated list of keys.
// It uses the seed and current time to rotate through keys.
func PickKey(s string, seed string) string {
	lines := strings.Split(s, "\n")
	var keys []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			keys = append(keys, line)
		}
	}

	if len(keys) == 0 {
		return ""
	}
	if len(keys) == 1 {
		return keys[0]
	}

	// For multiple keys, rotate.
	// Use seed + current time for rotation to ensure it changes for each request
	// but remains somewhat stable if called very closely with the same seed.
	entropy := fmt.Sprintf("%s-%d", seed, time.Now().UnixNano())
	hash := sha256.Sum256([]byte(entropy))
	var val uint64
	for i := 0; i < 8; i++ {
		val = (val << 8) | uint64(hash[i])
	}
	return keys[val%uint64(len(keys))]
}
