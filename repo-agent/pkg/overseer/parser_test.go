package overseer

import (
	"testing"
)

func TestParseAgentDefinition(t *testing.T) {
	content := `---
name: "Bug Fixer"
triggers:
  - type: issue
    action: opened
    labels: ["bug"]
---
You are a bug fixer.
`
	def, err := ParseAgentDefinition([]byte(content))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if def.Name != "Bug Fixer" {
		t.Errorf("expected name 'Bug Fixer', got '%s'", def.Name)
	}

	if len(def.Triggers) != 1 {
		t.Errorf("expected 1 trigger, got %d", len(def.Triggers))
	}

	if def.Triggers[0].Type != "issue" {
		t.Errorf("expected trigger type 'issue', got '%s'", def.Triggers[0].Type)
	}

	if def.Prompt != "You are a bug fixer." {
		t.Errorf("expected prompt 'You are a bug fixer.', got '%s'", def.Prompt)
	}
}
