package tasks

import (
	"bytes"
	_ "embed"
	"fmt"
)

// The exploration skill: one contract for the docs-exploration/ tree,
// shared by batch explore tasks and interactive chat sessions (the script
// materializes it into the checkout, so both engines discover it —
// claude via .claude/skills/, gemini via a GEMINI.md import).
//
//go:embed explore_skill.md
var exploreSkill []byte

// GetExploreSkill returns the exploration SKILL.md payload the command
// writes into the task dir for the script to materialize.
func GetExploreSkill() []byte {
	return exploreSkill
}

// GetExploreScript returns the in-sandbox exploration task script.
func GetExploreScript() ([]byte, error) {
	return getScriptWithDefaults("explore.sh")
}

// ExploreParams parameterize the exploration prompts.
type ExploreParams struct {
	RepoName string
	HTMLURL  string
	Topic    string // topic kind only
	Since    string // activity kind only, e.g. "2 weeks"
	Scenario string // runbook kind only, e.g. "deploy", "upgrade"
	Guidance string // runbook kind only: owner's free-text targets/constraints
}

// RenderExplorePrompt renders the prompt for an exploration kind:
// onboard (foundation docs), activity (recent-window digest + maintainer
// asks), topic (free-form deep dive).
func RenderExplorePrompt(kind string, params ExploreParams) ([]byte, error) {
	name := map[string]string{
		"onboard":  "explore_onboard.txt",
		"activity": "explore_activity.txt",
		"topic":    "explore_topic.txt",
		"runbook":  "explore_runbook.txt",
	}[kind]
	if name == "" {
		return nil, fmt.Errorf("unknown exploration kind %q (onboard|activity|topic|runbook)", kind)
	}
	t, err := getPromptTemplate(name)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, params); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
