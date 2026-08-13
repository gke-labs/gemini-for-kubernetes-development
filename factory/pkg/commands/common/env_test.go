package common

import (
	"os"
	"testing"
	"time"
)

func TestGetEnvDuration(t *testing.T) {
	key := "TEST_ENV_DURATION_VAR"
	defer os.Unsetenv(key)

	// Test default when unset
	os.Unsetenv(key)
	if val := GetEnvDuration(key, 5*time.Minute); val != 5*time.Minute {
		t.Errorf("GetEnvDuration() = %v, want %v", val, 5*time.Minute)
	}

	// Test valid duration
	os.Setenv(key, "30s")
	if val := GetEnvDuration(key, 5*time.Minute); val != 30*time.Second {
		t.Errorf("GetEnvDuration() = %v, want %v", val, 30*time.Second)
	}

	// Test invalid duration fallback
	os.Setenv(key, "invalid-duration")
	if val := GetEnvDuration(key, 10*time.Minute); val != 10*time.Minute {
		t.Errorf("GetEnvDuration() = %v, want %v", val, 10*time.Minute)
	}
}
