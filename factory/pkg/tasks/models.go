package tasks

import "strings"

// DefaultModels is the list of Gemini models to use, in order of preference and fallback.
var DefaultModels = []string{
	"gemini-3.5-flash",
	"gemini-3-flash-preview",
	"gemini-3.1-pro-preview",
	"gemini-2.5-pro",
}

// DefaultModelsString returns DefaultModels as a space-separated string for environment variables.
func DefaultModelsString() string {
	return strings.Join(DefaultModels, " ")
}
