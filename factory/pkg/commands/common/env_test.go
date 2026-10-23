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

func TestGetEnvInt(t *testing.T) {
	key := "TEST_ENV_INT_VAR"
	defer os.Unsetenv(key)

	// Test default when unset
	os.Unsetenv(key)
	if val := GetEnvInt(key, 85); val != 85 {
		t.Errorf("GetEnvInt() = %v, want 85", val)
	}

	// Test valid integer
	os.Setenv(key, "90")
	if val := GetEnvInt(key, 85); val != 90 {
		t.Errorf("GetEnvInt() = %v, want 90", val)
	}

	// Test invalid int fallback
	os.Setenv(key, "not-an-int")
	if val := GetEnvInt(key, 85); val != 85 {
		t.Errorf("GetEnvInt() = %v, want 85", val)
	}
}
