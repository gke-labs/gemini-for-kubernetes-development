package commands

import (
	"testing"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/common"
)

// A skipPR agent keeps nothing, even on a PR; on a PR the changes go to
// it; a workflow keeps its journal; anything else is a chore.
func TestAgentMode(t *testing.T) {
	for _, tc := range []struct {
		def  common.AgentDefinition
		pr   bool
		want string
	}{
		{common.AgentDefinition{}, false, "chore"},
		{common.AgentDefinition{}, true, "pr"},
		{common.AgentDefinition{SkipPR: true}, true, "none"},
		{common.AgentDefinition{Mode: "workflow"}, false, "workflow"},
		{common.AgentDefinition{Mode: "workflow", SkipPR: true}, false, "none"},
	} {
		if got := agentMode(&tc.def, tc.pr); got != tc.want {
			t.Errorf("agentMode(%+v, pr %v) = %s, want %s", tc.def, tc.pr, got, tc.want)
		}
	}
}
