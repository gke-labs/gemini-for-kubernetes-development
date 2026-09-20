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

// getScriptWithDefaults renders a task script: the shared lib.sh prelude
// (setupGit, configureGemini, record_gemini_usage, …) is prepended, the
// task script follows — bash keeps the last definition, so a script that
// needs different behavior simply redefines the function — and the
// __DEFAULT_MODELS__ placeholder is replaced with the default model list.
func getScriptWithDefaults(name string) ([]byte, error) {
	lib, err := scriptsFS.ReadFile("lib.sh")
	if err != nil {
		return nil, err
	}
	data, err := scriptsFS.ReadFile(name)
	if err != nil {
		return nil, err
	}
	content := string(lib) + "\n" + string(data)
	content = strings.ReplaceAll(content, "__DEFAULT_MODELS__", DefaultModelsString())
	return []byte(content), nil
}
