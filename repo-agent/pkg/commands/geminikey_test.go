package commands

import (
	"os"
	"testing"
)

func TestGetGeminiAPIKey(t *testing.T) {
	tests := []struct {
		name     string
		envKey   string
		expected string
	}{
		{
			name:     "Single key",
			envKey:   "key1",
			expected: "key1",
		},
		{
			name:     "Comma separated",
			envKey:   "key1,key2,key3",
			expected: "key1,key2,key3",
		},
		{
			name:     "Space separated",
			envKey:   "key1 key2 key3",
			expected: "key1,key2,key3",
		},
		{
			name:     "Newline separated",
			envKey:   "key1\nkey2\nkey3",
			expected: "key1,key2,key3",
		},
		{
			name:     "Mixed separators",
			envKey:   "key1, key2\nkey3\tkey4",
			expected: "key1,key2,key3,key4",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Setenv("GEMINI_API_KEY", tt.envKey)
			defer os.Unsetenv("GEMINI_API_KEY")

			got, err := GetGeminiAPIKey("test-seed")
			if err != nil {
				t.Fatalf("GetGeminiAPIKey() error = %v", err)
			}
			if got != tt.expected {
				t.Errorf("GetGeminiAPIKey() = %v, want %v", got, tt.expected)
			}
		})
	}
}
