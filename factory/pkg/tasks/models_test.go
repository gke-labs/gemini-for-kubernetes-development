package tasks

import (
	"strings"
	"testing"
)

func TestDefaultModels(t *testing.T) {
	if len(DefaultModels) == 0 {
		t.Error("DefaultModels list should not be empty")
	}

	if DefaultModels[0] != "gemini-3.6-flash" {
		t.Errorf("Expected first default model to be gemini-3.6-flash, got: %s", DefaultModels[0])
	}
}

func TestGetScriptWithDefaults(t *testing.T) {
	// Let's test that GetFixIssueScript successfully replaced the __DEFAULT_MODELS__ placeholder
	scriptBytes, err := GetFixIssueScript()
	if err != nil {
		t.Fatalf("Failed to get fix_issue script: %v", err)
	}

	scriptContent := string(scriptBytes)
	if strings.Contains(scriptContent, "__DEFAULT_MODELS__") {
		t.Error("Expected script to not contain '__DEFAULT_MODELS__' placeholder")
	}

	expectedString := DefaultModelsString()
	if !strings.Contains(scriptContent, expectedString) {
		t.Errorf("Expected script to contain default models list string: %s", expectedString)
	}
}
