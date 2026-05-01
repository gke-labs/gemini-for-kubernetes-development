package sandbox

import (
	"testing"
)

func TestProjectedTokens(t *testing.T) {
	tests := []struct {
		name                string
		llmAPIKeySecretName string
		expectedSources     []string
	}{
		{
			name:                "DefaultSecrets",
			llmAPIKeySecretName: "gemini-vscode-tokens",
			expectedSources:     []string{"gemini-vscode-tokens", "anthropic-api-key"},
		},
		{
			name:                "CustomSecret",
			llmAPIKeySecretName: "custom-secret",
			expectedSources:     []string{"custom-secret", "gemini-vscode-tokens", "anthropic-api-key"},
		},
		{
			name:                "ClaudeSecret",
			llmAPIKeySecretName: "anthropic-api-key",
			expectedSources:     []string{"anthropic-api-key", "gemini-vscode-tokens"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opt := AgentSandboxOptions{
				DevSandboxOptions: DevSandboxOptions{
					Name:                "test",
					Namespace:           "default",
					LLMAPIKeySecretName: tt.llmAPIKeySecretName,
				},
			}
			sandbox, _ := NewAgentSandbox(opt)

			spec := sandbox.Object["spec"].(map[string]interface{})
			podTemplate := spec["podTemplate"].(map[string]interface{})
			podSpec := podTemplate["spec"].(map[string]interface{})
			volumes := podSpec["volumes"].([]interface{})

			var tokensVol map[string]interface{}
			for _, v := range volumes {
				vol := v.(map[string]interface{})
				if vol["name"] == "tokens-secret" {
					tokensVol = vol
					break
				}
			}

			if tokensVol == nil {
				t.Fatalf("tokens-secret volume not found")
			}

			projected, ok := tokensVol["projected"].(map[string]interface{})
			if !ok {
				t.Fatalf("tokens-secret volume is not projected")
			}

			sources := projected["sources"].([]interface{})
			if len(sources) != len(tt.expectedSources) {
				t.Errorf("expected %d sources, got %d", len(tt.expectedSources), len(sources))
			}

			for i, expected := range tt.expectedSources {
				if i >= len(sources) {
					break
				}
				source := sources[i].(map[string]interface{})
				secret := source["secret"].(map[string]interface{})
				if secret["name"] != expected {
					t.Errorf("expected source %d to be %s, got %s", i, expected, secret["name"])
				}
			}
		})
	}
}

func TestEnvAPIKeys(t *testing.T) {
	opt := AgentSandboxOptions{
		DevSandboxOptions: DevSandboxOptions{
			Name:      "test",
			Namespace: "default",
		},
	}
	sandbox, _ := NewAgentSandbox(opt)

	spec := sandbox.Object["spec"].(map[string]interface{})
	podTemplate := spec["podTemplate"].(map[string]interface{})
	podSpec := podTemplate["spec"].(map[string]interface{})
	containers := podSpec["containers"].([]interface{})
	container := containers[0].(map[string]interface{})
	env := container["env"].([]interface{})

	expectedEnv := map[string]string{
		"GEMINI_API_KEY":    "gemini-vscode-tokens",
		"ANTHROPIC_API_KEY": "anthropic-api-key",
	}

	for name, secretName := range expectedEnv {
		found := false
		for _, e := range env {
			envVar := e.(map[string]interface{})
			if envVar["name"] == name {
				found = true
				valueFrom := envVar["valueFrom"].(map[string]interface{})
				secretKeyRef := valueFrom["secretKeyRef"].(map[string]interface{})
				if secretKeyRef["name"] != secretName {
					t.Errorf("expected env %s to use secret %s, got %s", name, secretName, secretKeyRef["name"])
				}
			}
		}
		if !found {
			t.Errorf("env var %s not found", name)
		}
	}
}
