package tasks

import (
	"strings"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/geminitokens"
)

// DefaultModels is the list of Gemini models to use, in order of preference and fallback.
var DefaultModels = geminitokens.DefaultModels

// DefaultModelsString returns DefaultModels as a space-separated string for environment variables.
func DefaultModelsString() string {
	return strings.Join(DefaultModels, " ")
}

// GetAvailableModelsForKey returns a space-separated string of models whose quota is not exceeded for the given key.
func GetAvailableModelsForKey(key string) string {
	activeModels := geminitokens.GetAvailableModels(key, DefaultModels)
	// If all models are marked as quota exceeded, fall back to the full list so we don't pass an empty string
	if len(activeModels) == 0 {
		return DefaultModelsString()
	}
	return strings.Join(activeModels, " ")
}

// getScriptWithDefaults reads the specified script from scriptsFS and replaces
// the __DEFAULT_MODELS__ placeholder with the space-separated list of default models.
func getScriptWithDefaults(name string) ([]byte, error) {
	data, err := scriptsFS.ReadFile(name)
	if err != nil {
		return nil, err
	}
	content := string(data)
	content = strings.ReplaceAll(content, "__DEFAULT_MODELS__", DefaultModelsString())
	return []byte(content), nil
}
