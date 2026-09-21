package tasks

import (
	"strings"
	"testing"
)

// The three exploration prompts render with their parameters and all
// point the engine at the skill contract; unknown kinds fail fast.
func TestRenderExplorePrompt(t *testing.T) {
	params := ExploreParams{RepoName: "agent-sandbox", HTMLURL: "https://github.com/kubernetes-sigs/agent-sandbox", Topic: "compare with gVisor", Since: "1 month"}
	for kind, want := range map[string]string{
		"onboard":  "overview.md",
		"activity": "1 month",
		"topic":    "compare with gVisor",
	} {
		out, err := RenderExplorePrompt(kind, params)
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		s := string(out)
		if !strings.Contains(s, want) || !strings.Contains(s, "agent-sandbox") || !strings.Contains(s, "SKILL.md") {
			t.Errorf("%s prompt missing expectations:\n%s", kind, s)
		}
		if !strings.Contains(s, "Do not commit or push") {
			t.Errorf("%s prompt must leave commit/push to the harness", kind)
		}
	}
	if _, err := RenderExplorePrompt("nonsense", params); err == nil {
		t.Error("unknown kind must error")
	}
	if len(GetExploreSkill()) == 0 || !strings.Contains(string(GetExploreSkill()), "docs-exploration/") {
		t.Error("embedded skill must carry the docs tree contract")
	}
}
