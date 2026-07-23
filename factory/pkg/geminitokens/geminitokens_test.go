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
			name:     "fatal quota exhaustion even with backoff retry log",
			input:    `Attempt 2 failed with status 429. Retrying with backoff... _ApiError: {"error":{"message":"You exceeded your current quota, please check your plan and billing details."}}`,
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
		{
			name:     "fatal billing quota log with backoff retry",
			input:    `Attempt 2 failed with status 429. Retrying with backoff... _ApiError: {"error":{"message":"You exceeded your current quota, please check your plan and billing details."}}`,
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
