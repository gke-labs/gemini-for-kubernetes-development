package commands

import (
	"os"
	"testing"
)

func TestGetMaxReviewFiles(t *testing.T) {
	// Test default value
	os.Unsetenv("MAX_REVIEW_FILES")
	if val := getMaxReviewFiles(); val != DefaultMaxReviewFiles {
		t.Errorf("Expected default %d, got %d", DefaultMaxReviewFiles, val)
	}

	// Test valid value
	os.Setenv("MAX_REVIEW_FILES", "50")
	if val := getMaxReviewFiles(); val != 50 {
		t.Errorf("Expected 50, got %d", val)
	}

	// Test invalid value (should fallback to default)
	os.Setenv("MAX_REVIEW_FILES", "invalid")
	if val := getMaxReviewFiles(); val != DefaultMaxReviewFiles {
		t.Errorf("Expected fallback to default %d, got %d", DefaultMaxReviewFiles, val)
	}

	// Cleanup
	os.Unsetenv("MAX_REVIEW_FILES")
}
