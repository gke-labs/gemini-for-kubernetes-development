package tasks

import (
	"strings"
	"testing"
)

func TestRenderRunAgentPrecondition(t *testing.T) {
	params := PreconditionParams{
		AgentName:         "test-agent",
		AgentPrecondition: "Make sure all tests pass",
		GithubContext:     "Issue context goes here",
	}

	res, err := RenderRunAgentPrecondition(params)
	if err != nil {
		t.Fatalf("failed to render run agent precondition: %v", err)
	}

	content := string(res)
	if !strings.Contains(content, "test-agent") {
		t.Errorf("expected rendered text to contain AgentName, got: %s", content)
	}
	if !strings.Contains(content, "Make sure all tests pass") {
		t.Errorf("expected rendered text to contain AgentPrecondition, got: %s", content)
	}
	if !strings.Contains(content, "Issue context goes here") {
		t.Errorf("expected rendered text to contain GithubContext, got: %s", content)
	}
}
