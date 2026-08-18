package tasks

import "strings"

// DefaultModels is the list of Gemini models to use, in order of preference and fallback.
var DefaultModels = []string{
	"gemini-3.7-flash",
	"gemini-3.6-flash",
	"gemini-3.5-flash",
	"gemini-3-flash-preview",
	"gemini-3.1-pro-preview",
	"gemini-2.5-pro",
	"gemini-2.5-flash",
}

// DefaultModelsString returns DefaultModels as a space-separated string for environment variables.
func DefaultModelsString() string {
	return strings.Join(DefaultModels, " ")
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
