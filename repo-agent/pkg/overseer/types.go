package overseer

// AgentDefinition represents an agent defined in the .agent/ folder.
type AgentDefinition struct {
	Name        string   `json:"name,omitempty"`
	Description string   `json:"description,omitempty"`
	Triggers    []Trigger `json:"triggers,omitempty"`
	Prompt      string   `json:"prompt,omitempty"`
}

// Trigger defines when an agent should run.
type Trigger struct {
	// Type of trigger (e.g., "issue", "pull_request", "comment")
	Type string `json:"type,omitempty"`
	// Action (e.g., "opened", "synchronize", "created")
	Action string `json:"action,omitempty"`
	// Labels required for this trigger
	Labels []string `json:"labels,omitempty"`
	// Branch required for this trigger (e.g., "main")
	Branch string `json:"branch,omitempty"`
}
