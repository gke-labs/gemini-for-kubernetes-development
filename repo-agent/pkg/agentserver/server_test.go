package agentserver

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

func TestIsProcessAliveAndNotZombie(t *testing.T) {
	// 1. Current running process with matching start time
	pid := os.Getpid()
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "lstart=").Output()
	if err != nil {
		t.Fatalf("failed to get start time: %v", err)
	}
	startTime := strings.Join(strings.Fields(string(out)), " ")

	if !isProcessAliveAndNotZombie(pid, startTime) {
		t.Errorf("expected current process %d with matching start time to be alive", pid)
	}

	// 2. Current running process with empty expected start time (fallback)
	if !isProcessAliveAndNotZombie(pid, "") {
		t.Errorf("expected current process %d with empty start time to be alive", pid)
	}

	// 3. Current running process with mismatched start time (PID recycling test)
	if isProcessAliveAndNotZombie(pid, "Thu Jan 1 00:00:00 1970") {
		t.Errorf("expected process %d with mismatched start time to be considered not alive", pid)
	}

	// 4. Non-existent PID
	if isProcessAliveAndNotZombie(9999999, "") {
		t.Errorf("expected non-existent PID to be considered not alive")
	}
}
