package commands

import (
	"testing"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/common"
)

// A skipPR agent keeps nothing, a workflow keeps its journal, anything
// else is a chore.
func TestAgentMode(t *testing.T) {
	for _, tc := range []struct {
		def  common.AgentDefinition
		want string
	}{
		{common.AgentDefinition{}, "chore"},
		{common.AgentDefinition{SkipPR: true}, "none"},
		{common.AgentDefinition{Mode: "workflow"}, "workflow"},
		{common.AgentDefinition{Mode: "workflow", SkipPR: true}, "none"},
	} {
		if got := agentMode(&tc.def); got != tc.want {
			t.Errorf("agentMode(%+v) = %s, want %s", tc.def, got, tc.want)
		}
	}
}
