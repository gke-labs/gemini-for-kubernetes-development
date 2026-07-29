package geminitokens

import "testing"

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
	testKey := "AIzaSyTEST_SUSPENDED_KEY_12345"
	if err := AddSuspendedKey(testKey); err != nil {
		t.Fatalf("AddSuspendedKey failed: %v", err)
	}

	if !IsKeySuspended(testKey) {
		t.Errorf("IsKeySuspended(%q) expected true, got false", testKey)
	}

	if !IsKeyQuotaExceeded(testKey) {
		t.Errorf("IsKeyQuotaExceeded(%q) expected true as fallback, got false", testKey)
	}
}
