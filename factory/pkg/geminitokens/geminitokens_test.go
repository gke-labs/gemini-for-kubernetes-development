package geminitokens

import (
	"os"
	"testing"
	"time"
)

func TestIsFatalQuotaError(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{
			name:     "transient 429 with backoff retry",
			input:    `Attempt 1 failed with status 429. Retrying with backoff... _ApiError: {"error":{"message":"RESOURCE_EXHAUSTED"}}`,
			expected: false,
		},
		{
			name:     "true daily billing quota exhaustion",
			input:    `You exceeded your current quota, please check your plan and billing details.`,
			expected: true,
		},
		{
			name:     "max retries exceeded after transient retries",
			input:    `Max retries exceeded for status: 429`,
			expected: true,
		},
		{
			name:     "unhandled 429 without backoff",
			input:    `Error: status: 429 Too Many Requests`,
			expected: true,
		},
		{
			name:     "fatal billing quota during retry attempt",
			input:    `Attempt 3 failed with status 429. Retrying with backoff... _ApiError: {"error":{"message":"You exceeded your current quota, please check your plan and billing details.","code":429,"status":"Too Many Requests"}}`,
			expected: true,
		},
		{
			name:     "normal output",
			input:    `Generated code successfully`,
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := IsFatalQuotaError([]byte(tc.input))
			if got != tc.expected {
				t.Errorf("IsFatalQuotaError(%q) = %v, want %v", tc.input, got, tc.expected)
			}
		})
	}
}

func TestIsTransientRateLimit(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{
			name:     "transient retry log",
			input:    `Attempt 1 failed with status 429. Retrying with backoff...`,
			expected: true,
		},
		{
			name:     "fatal billing quota log",
			input:    `You exceeded your current quota, please check your plan and billing details.`,
			expected: false,
		},
		{
			name:     "fatal billing quota during retry attempt",
			input:    `Attempt 3 failed with status 429. Retrying with backoff... _ApiError: {"error":{"message":"You exceeded your current quota, please check your plan and billing details.","code":429,"status":"Too Many Requests"}}`,
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := IsTransientRateLimit([]byte(tc.input))
			if got != tc.expected {
				t.Errorf("IsTransientRateLimit(%q) = %v, want %v", tc.input, got, tc.expected)
			}
		})
	}
}

func TestIsSuspendedKeyError(t *testing.T) {
	suspendedPayload := `{"error":{"type":"Error","message":"{\"error\":{\"message\":\"{\\n  \\\"error\\\": {\\n    \\\"code\\\": 403,\\n    \\\"message\\\": \\\"Permission denied: Consumer 'api_key:AIzaSyDUMMY_SUSPENDED_TOKEN_FOR_TESTING_12345' has been suspended.\\\",\\n    \\\"status\\\": \\\"PERMISSION_DENIED\\\",\\n    \\\"details\\\": [\\n      {\\n        \\\"@type\\\": \\\"type.googleapis.com/google.rpc.ErrorInfo\\\",\\n        \\\"reason\\\": \\\"CONSUMER_SUSPENDED\\\",\\n        \\\"domain\\\": \\\"googleapis.com\\\",\\n        \\\"metadata\\\": {\\n          \\\"consumer\\\": \\\"projects/36948037873\\\",\\n          \\\"containerInfo\\\": \\\"api_key:AIzaSyDUMMY_SUSPENDED_TOKEN_FOR_TESTING_12345\\\",\\n          \\\"service\\\": \\\"generativelanguage.googleapis.com\\\"\\n        }\\n      }\\n    ]\\n  }\\n}\\n\",\"code\":403,\"status\":\"Forbidden\"}}","code":403}}`

	if !IsSuspendedKeyError([]byte(suspendedPayload)) {
		t.Errorf("IsSuspendedKeyError expected true for CONSUMER_SUSPENDED payload, got false")
	}

	if !IsFatalQuotaError([]byte(suspendedPayload)) {
		t.Errorf("IsFatalQuotaError expected true for CONSUMER_SUSPENDED payload, got false")
	}
}

func TestExtractAPIKeyFromError(t *testing.T) {
	suspendedPayload := `Permission denied: Consumer 'api_key:AIzaSyDUMMY_SUSPENDED_TOKEN_FOR_TESTING_12345' has been suspended.`
	extracted := ExtractAPIKeyFromError([]byte(suspendedPayload))
	expected := "AIzaSyDUMMY_SUSPENDED_TOKEN_FOR_TESTING_12345"

	if extracted != expected {
		t.Errorf("ExtractAPIKeyFromError got %q, want %q", extracted, expected)
	}
}

func TestAddSuspendedKey(t *testing.T) {
	_ = os.Remove(getQuotaExceededFilePath())
	_ = os.Remove(getSuspendedFilePath())
	defer func() {
		_ = os.Remove(getQuotaExceededFilePath())
		_ = os.Remove(getSuspendedFilePath())
	}()

	testKey := "AIzaSyDUMMY_FULL_SUSPENDED_KEY_99999"
	if err := AddSuspendedKey(testKey); err != nil {
		t.Fatalf("AddSuspendedKey failed: %v", err)
	}

	if !IsKeySuspended(testKey) {
		t.Errorf("IsKeySuspended(%q) expected true, got false", testKey)
	}

	status, err := GetTokensStatus()
	if err != nil {
		t.Fatalf("GetTokensStatus failed: %v", err)
	}

	expectedObscured := testKey
	if len(expectedObscured) > 8 {
		expectedObscured = expectedObscured[:8] + "..."
	}

	found := false
	for _, key := range status.SuspendedList {
		if key == expectedObscured {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("GetTokensStatus().SuspendedList expected to contain obscured key %q, got %v", expectedObscured, status.SuspendedList)
	}
}

func TestModelQuotaExceeded(t *testing.T) {
	_ = os.Remove(getQuotaExceededFilePath())
	_ = os.Remove(getSuspendedFilePath())
	defer func() {
		_ = os.Remove(getQuotaExceededFilePath())
		_ = os.Remove(getSuspendedFilePath())
	}()

	key := "AIzaSyDUMMY_KEY_FOR_MODEL_TEST_123"
	model := "gemini-3.6-flash"

	// 1. Initial check (no quota exceeded)
	if IsKeyModelQuotaExceeded(key, model) {
		t.Errorf("IsKeyModelQuotaExceeded expected false, got true")
	}

	// 2. Add quota exceeded for specific model
	if err := AddQuotaExceededKeyAndModel(key, model, 2*time.Hour); err != nil {
		t.Fatalf("AddQuotaExceededKeyAndModel failed: %v", err)
	}

	// 3. Verify specific model is exceeded
	if !IsKeyModelQuotaExceeded(key, model) {
		t.Errorf("IsKeyModelQuotaExceeded for model expected true, got false")
	}

	// 4. Verify another model is NOT exceeded
	if IsKeyModelQuotaExceeded(key, "gemini-3.5-flash") {
		t.Errorf("IsKeyModelQuotaExceeded for different model expected false, got true")
	}

	// 5. Verify that IsKeyAllModelsQuotaExceeded returns false initially because only one model is exceeded
	if IsKeyAllModelsQuotaExceeded(key) {
		t.Errorf("IsKeyAllModelsQuotaExceeded expected false (only one model exceeded), got true")
	}

	// 6. Add quota exceeded for all DefaultModels
	for _, m := range DefaultModels {
		if err := AddQuotaExceededKeyAndModel(key, m, 2*time.Hour); err != nil {
			t.Fatalf("AddQuotaExceededKeyAndModel failed for %s: %v", m, err)
		}
	}

	// 7. Verify IsKeyAllModelsQuotaExceeded is now true
	if !IsKeyAllModelsQuotaExceeded(key) {
		t.Errorf("IsKeyAllModelsQuotaExceeded expected true (all models exceeded), got false")
	}
}

func TestModelQuotaExceededFallbackAndLegacy(t *testing.T) {
	_ = os.Remove(getQuotaExceededFilePath())
	_ = os.Remove(getSuspendedFilePath())
	defer func() {
		_ = os.Remove(getQuotaExceededFilePath())
		_ = os.Remove(getSuspendedFilePath())
	}()

	keyFallback := "AIzaSyDUMMY_FALLBACK_KEY_888"

	// Test 1: model == "" fallback -> marks all default models as exceeded
	if err := AddQuotaExceededKeyAndModel(keyFallback, "", 1*time.Hour); err != nil {
		t.Fatalf("AddQuotaExceededKeyAndModel with empty model failed: %v", err)
	}

	for _, m := range DefaultModels {
		if !IsKeyModelQuotaExceeded(keyFallback, m) {
			t.Errorf("Expected model %s to be exceeded due to empty model fallback", m)
		}
	}

	// Test 2: Legacy fallback check -> key stored directly without colon suffix
	keyLegacy := "AIzaSyDUMMY_LEGACY_KEY_777"
	listMutex.Lock()
	list, err := loadQuotaExceededList()
	if err != nil {
		list = make(map[string]time.Time)
	}
	list[keyLegacy] = time.Now().Add(1 * time.Hour)
	_ = saveQuotaExceededList(list)
	listMutex.Unlock()

	// Verify IsKeyModelQuotaExceeded detects the legacy fallback for any model
	for _, m := range DefaultModels {
		if !IsKeyModelQuotaExceeded(keyLegacy, m) {
			t.Errorf("Expected model %s to be exceeded due to legacy fallback", m)
		}
	}
}

func TestGetTokensStatusDegraded(t *testing.T) {
	_ = os.Remove(getQuotaExceededFilePath())
	_ = os.Remove(getSuspendedFilePath())
	defer func() {
		_ = os.Remove(getQuotaExceededFilePath())
		_ = os.Remove(getSuspendedFilePath())
	}()

	// Mock TOKENSCRIPT_DIR environment variable
	tempDir, err := os.MkdirTemp("", "tokenscript-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	oldDir := os.Getenv("TOKENSCRIPT_DIR")
	defer os.Setenv("TOKENSCRIPT_DIR", oldDir)
	os.Setenv("TOKENSCRIPT_DIR", tempDir)

	// Create a mock token script
	scriptContent := `#!/bin/sh
state_file="` + tempDir + `/state"
if [ ! -f "$state_file" ]; then
  echo "AIzaSyDUMMY_HEALTHY_KEY_001"
  touch "$state_file"
else
  echo "AIzaSyDUMMY_DEGRADED_KEY_002"
  rm -f "$state_file"
fi
`
	scriptPath := tempDir + "/get-token"
	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0755); err != nil {
		t.Fatalf("Failed to write mock script: %v", err)
	}

	// AIzaSyDUMMY_DEGRADED_KEY_002 will be exceeded only for gemini-3.6-flash
	if err := AddQuotaExceededKeyAndModel("AIzaSyDUMMY_DEGRADED_KEY_002", "gemini-3.6-flash", 1*time.Hour); err != nil {
		t.Fatalf("AddQuotaExceededKeyAndModel failed: %v", err)
	}

	status, err := GetTokensStatus()
	if err != nil {
		t.Fatalf("GetTokensStatus failed: %v", err)
	}

	// AIzaSyDUMMY_HEALTHY_KEY_001 should be Active (Healthy)
	foundHealthy := false
	for _, k := range status.ActiveList {
		if k == "AIzaSyDU..." {
			foundHealthy = true
			break
		}
	}
	if !foundHealthy {
		t.Errorf("Expected ActiveList to contain 'AIzaSyDU...', got %v", status.ActiveList)
	}

	// AIzaSyDUMMY_DEGRADED_KEY_002 should be Active (Degraded)
	foundDegraded := false
	for _, k := range status.ActiveList {
		if k == "AIzaSyDU... (Degraded: exceeded gemini-3.6-flash)" {
			foundDegraded = true
			break
		}
	}
	if !foundDegraded {
		t.Errorf("Expected ActiveList to contain 'AIzaSyDU... (Degraded: exceeded gemini-3.6-flash)', got %v", status.ActiveList)
	}
}
