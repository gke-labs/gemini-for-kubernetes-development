package commands

import (
	"testing"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/common"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/usagereport"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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

// A workflow run's usage record carries its session and name, and the
// session is the one the usage sweep reads off its sandbox: one
// workflow's usage, whichever reported it.
func TestWorkflowUsage(t *testing.T) {
	m := usagereport.Meta{Repo: "o/r", TaskType: "workflow", Issue: 7}
	workflowUsage("issue-7", "checklist-for-kind")(&m)
	if m.Workflow != "issue-7" || m.WorkflowName != "checklist-for-kind" || m.Issue != 7 || m.TaskType != "workflow" {
		t.Errorf("meta %+v", m)
	}
	sb := &unstructured.Unstructured{}
	sb.SetName("wf-issue-7")
	if swept := common.SandboxUsageMeta(sb, "o/r"); swept.Workflow != m.Workflow || swept.Issue != m.Issue {
		t.Errorf("the sweep reads %+v off the sandbox, the run reports %+v", swept, m)
	}
}
