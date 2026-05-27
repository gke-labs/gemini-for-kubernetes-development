package tasks

import (
	"bytes"
	"fmt"
)

// AgentParams represents the parameters passed to the agent run script.
type AgentParams struct {
	AgentPrompt string
	AgentName   string
	AgentFile   string
	RepoName    string
	CloneURL    string
	RepoOwner   string
	PromptFile  string
	SkipPR      bool
	PRNumber    int
	Models      []string
}

// GetRunAgentScript returns the embedded run_agent.sh script.
func GetRunAgentScript() ([]byte, error) {
	return scriptsFS.ReadFile("run_agent.sh")
}

// RenderRunAgentPrompt executes the run_agent.txt template with the given parameters.
func RenderRunAgentPrompt(params AgentParams) ([]byte, error) {
	if len(params.Models) == 0 {
		params.Models = []string{"gemini-3.5-flash", "gemini-3-flash-preview", "gemini-3.1-pro-preview", "gemini-2.5-pro"}
	}

	promptTmpl, err := getPromptTemplate("run_agent.txt")
	if err != nil {
		return nil, fmt.Errorf("getting prompt template: %w", err)
	}
	var pBuf bytes.Buffer
	if err := promptTmpl.Execute(&pBuf, params); err != nil {
		return nil, fmt.Errorf("executing prompt template: %w", err)
	}

	return pBuf.Bytes(), nil
}
