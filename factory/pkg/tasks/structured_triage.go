package tasks

// TriageSuggestion is the structured triage produced for an issue.
type TriageSuggestion struct {
	// Labels suggested for the issue, drawn from the repo's existing labels.
	Labels []string `yaml:"labels,omitempty" json:"labels,omitempty"`
	// Priority is low, medium, or high.
	Priority string `yaml:"priority,omitempty" json:"priority,omitempty"`
	// Duplicates lists issue numbers that look like duplicates.
	Duplicates []int `yaml:"duplicates,omitempty" json:"duplicates,omitempty"`
	// Assessment is a short summary: what the issue is, the likely cause or
	// affected area, and a suggested next step.
	Assessment string `yaml:"assessment,omitempty" json:"assessment,omitempty"`
}

// TriageAgentOutput defines the structure of the triage agent's YAML output.
type TriageAgentOutput struct {
	Triage *TriageSuggestion `yaml:"triage"`
}
