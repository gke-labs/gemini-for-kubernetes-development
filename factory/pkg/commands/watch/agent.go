package watch

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

type AgentDefinition struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Schedule    string `yaml:"schedule"`
	SkipPR      bool   `yaml:"skipPR,omitempty"`
	Mode        string `yaml:"mode,omitempty"`
	Cooldown    string `yaml:"cooldown,omitempty"`
	Prompt      string `yaml:"-"`
}

func ParseAgent(content []byte) (*AgentDefinition, error) {
	parts := strings.SplitN(string(content), "---", 3)
	if len(parts) < 3 {
		return nil, fmt.Errorf("invalid agent definition format: missing frontmatter")
	}

	var def AgentDefinition
	if err := yaml.Unmarshal([]byte(parts[1]), &def); err != nil {
		return nil, fmt.Errorf("failed to unmarshal frontmatter: %w", err)
	}

	def.Prompt = strings.TrimSpace(parts[2])
	return &def, nil
}
