package recipe

import (
	"strings"
	"testing"
)

// The repository's workspace-cleanup recipe when it has one, else the
// built-in, which deletes nothing; a cleanup recipe runs no agent.
func TestWorkspaceCleanup(t *testing.T) {
	r, _, source, err := WorkspaceCleanup(nil)
	if err != nil || source != "built-in" || len(r.Start.Steps) == 0 || r.Inputs["target_bytes"].Description == "" {
		t.Errorf("built-in: %q, %v, %+v", source, err, r)
	}
	if _, data, source, err := WorkspaceCleanup([]byte("name: workspace-cleanup\nstart:\n  steps:\n    - run: echo hi\n")); err != nil || source != WorkspaceCleanupPath || !strings.Contains(string(data), "echo hi") {
		t.Errorf("the repository's: %q, %v", source, err)
	}
	if _, _, _, err := WorkspaceCleanup([]byte("name: workspace-cleanup\nstart:\n  steps:\n    - ask: delete what you like\n")); err == nil || !strings.Contains(err.Error(), "runs no agent") {
		t.Errorf("an ask step: %v", err)
	}
}
