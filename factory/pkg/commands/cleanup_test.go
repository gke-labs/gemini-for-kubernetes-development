package commands

import (
	"context"
	"os"
	"testing"
)

func TestGetDiskUsagePercent(t *testing.T) {
	tmpDir := t.TempDir()
	usage, err := getDiskUsagePercent(tmpDir)
	if err != nil {
		t.Fatalf("getDiskUsagePercent(%q) returned error: %v", tmpDir, err)
	}
	if usage < 0 || usage > 100 {
		t.Errorf("getDiskUsagePercent(%q) = %f, expected between 0 and 100", tmpDir, usage)
	}
}

func TestCheckDiskUsageAndCleanupNoPanic(t *testing.T) {
	ctx := context.Background()
	// Should not panic even if /workspaces does not exist or threshold is exceeded
	os.Setenv("CLEANUP_DISK_USAGE_THRESHOLD_PERCENT", "0")
	defer os.Unsetenv("CLEANUP_DISK_USAGE_THRESHOLD_PERCENT")

	checkDiskUsageAndCleanup(ctx)
}
