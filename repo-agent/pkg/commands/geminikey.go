package commands

import (
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/tokens"
)

// GetGeminiAPIKey retrieves the Gemini API key from the environment variable.
// We also support executing a command to retrieve the key, if the value starts with "exec:".
func GetGeminiAPIKey(seed string) (string, error) {
	return tokens.GetRawGeminiAPIKey(seed)
}
