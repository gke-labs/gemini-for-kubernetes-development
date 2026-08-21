package agentserver

import (
	"os"
	"testing"
)

func TestIsProcessAliveAndNotZombie(t *testing.T) {
	// 1. Current running process
	pid := os.Getpid()
	if !isProcessAliveAndNotZombie(pid) {
		t.Errorf("expected current process %d to be alive", pid)
	}

	// 2. Non-existent PID
	if isProcessAliveAndNotZombie(9999999) {
		t.Errorf("expected non-existent PID to be considered not alive")
	}
}
