package agentoutput

import "github.com/google/go-github/v39/github"

// AgentOutput defines the common structure for agent outputs (labels, notes).
type AgentOutput struct {
	Labels []string `yaml:"labels,omitempty"`
	Note   string   `yaml:"note,omitempty"`
}

// ReviewAgentOutput defines the structure for the review agent's YAML output.
type ReviewAgentOutput struct {
	Labels []string                         `yaml:"labels,omitempty"`
	Note   string                           `yaml:"note,omitempty"`
	Review *github.PullRequestReviewRequest `yaml:"review,omitempty"`
}
