package overseer

import (
	"os/exec"
	"testing"
)

// The overseer image's queue-state.sh keeps the watch's queue state across
// a cycle that was cut short; its tests run with a throwaway clone and
// remote (images/overseer/queue-state_test.sh).
func TestQueueStateScript(t *testing.T) {
	for _, tool := range []string{"bash", "git", "pkill"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not found", tool)
		}
	}
	out, err := exec.Command("bash", "../../images/overseer/queue-state_test.sh").CombinedOutput()
	if err != nil {
		t.Fatalf("queue-state_test.sh: %v\n%s", err, out)
	}
}
