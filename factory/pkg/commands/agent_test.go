package commands

import (
	"testing"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/common"
)

// mode: workflow is a workflow, skipPR or not; anything else is a chore.
func TestIsWorkflow(t *testing.T) {
	for _, tc := range []struct {
		def  common.AgentDefinition
		want bool
	}{
		{common.AgentDefinition{}, false},
		{common.AgentDefinition{SkipPR: true}, false},
		{common.AgentDefinition{Mode: "workflow"}, true},
		{common.AgentDefinition{Mode: "workflow", SkipPR: true}, true},
	} {
		if got := isWorkflow(&tc.def); got != tc.want {
			t.Errorf("isWorkflow(%+v) = %v, want %v", tc.def, got, tc.want)
		}
	}
}
