package sandbox

import (
	"testing"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
)

func TestProjectedTokens(t *testing.T) {
	tests := []struct {
		name                string
		llmAPIKeySecretName string
		expectedSources     []string
	}{
		{
			name:                "DefaultSecrets",
			llmAPIKeySecretName: k8s.GeminiSecretName,
			expectedSources:     []string{k8s.GeminiSecretName, k8s.ClaudeSecretName},
		},
		{
			name:                "CustomSecret",
			llmAPIKeySecretName: "custom-secret",
			expectedSources:     []string{"custom-secret", k8s.GeminiSecretName, k8s.ClaudeSecretName},
		},
		{
			name:                "ClaudeSecret",
			llmAPIKeySecretName: k8s.ClaudeSecretName,
			expectedSources:     []string{k8s.ClaudeSecretName, k8s.GeminiSecretName},
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
			if sandbox == nil {
				t.Fatalf("NewAgentSandbox returned nil")
			}

			spec, ok := sandbox.Object["spec"].(map[string]interface{})
			if !ok {
				t.Fatalf("sandbox missing spec")
			}
			podTemplate, ok := spec["podTemplate"].(map[string]interface{})
			if !ok {
				t.Fatalf("spec missing podTemplate")
			}
			podSpec, ok := podTemplate["spec"].(map[string]interface{})
			if !ok {
				t.Fatalf("podTemplate missing spec")
			}
			volumes, ok := podSpec["volumes"].([]interface{})
			if !ok {
				t.Fatalf("podSpec missing volumes")
			}

			var tokensVol map[string]interface{}
			for _, v := range volumes {
				vol, ok := v.(map[string]interface{})
				if !ok {
					continue
				}
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
				source, ok := sources[i].(map[string]interface{})
				if !ok {
					t.Errorf("source %d is not a map", i)
					continue
				}
				secret, ok := source["secret"].(map[string]interface{})
				if !ok {
					t.Errorf("source %d is missing secret map", i)
					continue
				}
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
	sandbox, svc := NewAgentSandbox(opt)
	if sandbox == nil || svc == nil {
		t.Fatal("NewAgentSandbox returned nil")
	}

	spec, ok := sandbox.Object["spec"].(map[string]interface{})
	if !ok {
		t.Fatal("sandbox missing spec")
	}
	podTemplate, ok := spec["podTemplate"].(map[string]interface{})
	if !ok {
		t.Fatal("spec missing podTemplate")
	}
	podSpec, ok := podTemplate["spec"].(map[string]interface{})
	if !ok {
		t.Fatal("podTemplate missing spec")
	}
	containers, ok := podSpec["containers"].([]interface{})
	if !ok || len(containers) == 0 {
		t.Fatal("podSpec missing containers")
	}
	container, ok := containers[0].(map[string]interface{})
	if !ok {
		t.Fatal("container is not a map")
	}
	env, ok := container["env"].([]interface{})
	if !ok {
		t.Fatal("container missing env")
	}

	expectedEnv := map[string]string{
		"GEMINI_API_KEY":    k8s.GeminiSecretName,
		"ANTHROPIC_API_KEY": k8s.ClaudeSecretName,
	}

	for name, secretName := range expectedEnv {
		found := false
		for _, e := range env {
			envVar, ok := e.(map[string]interface{})
			if !ok {
				continue
			}
			if envVar["name"] == name {
				found = true
				valueFrom, ok := envVar["valueFrom"].(map[string]interface{})
				if !ok {
					t.Errorf("env var %s missing valueFrom", name)
					continue
				}
				secretKeyRef, ok := valueFrom["secretKeyRef"].(map[string]interface{})
				if !ok {
					t.Errorf("env var %s missing secretKeyRef", name)
					continue
				}
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
