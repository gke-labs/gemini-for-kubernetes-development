package commands

import (
	"strings"
	"testing"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/constants"
	corev1 "k8s.io/api/core/v1"
)

// applyEngineEnv: antigravity authenticates with the gemini key — agy in a
// pod has no sign-in to fall back on — so it gets GEMINI_API_KEY, leaves the
// model to agy, and fails before the task starts when the key is missing.
func TestApplyEngineEnvAntigravity(t *testing.T) {
	t.Setenv("TOKENSCRIPT_DIR", "")
	saved := rootFlags.Engine
	t.Cleanup(func() { rootFlags.Engine = saved })
	rootFlags.Engine = "antigravity"

	env := map[string]string{}
	secret := &corev1.Secret{Data: map[string][]byte{constants.KeyGeminiAPIKey: []byte("g-key")}}
	if err := applyEngineEnv(env, secret); err != nil {
		t.Fatalf("applyEngineEnv: %v", err)
	}
	if env["ENGINE"] != "antigravity" || env["GEMINI_API_KEY"] != "g-key" || env["MODELS"] != "default" {
		t.Errorf("env = %v, want ENGINE=antigravity GEMINI_API_KEY=g-key MODELS=default", env)
	}
	if _, ok := env["ANTHROPIC_API_KEY"]; ok {
		t.Errorf("antigravity must not be handed the anthropic key: %v", env)
	}

	err := applyEngineEnv(map[string]string{}, &corev1.Secret{Data: map[string][]byte{}})
	if err == nil || !strings.Contains(err.Error(), "GEMINI_API_KEY") {
		t.Errorf("missing key: err = %v, want a GEMINI_API_KEY error", err)
	}
}
