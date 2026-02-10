package overseer

import (
	"bytes"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// ParseAgentDefinition parses an agent definition from a markdown file with frontmatter.
func ParseAgentDefinition(content []byte) (*AgentDefinition, error) {
	// 1. Separate Frontmatter and Content
	parts := bytes.SplitN(content, []byte("---"), 3)
	if len(parts) < 3 {
		return nil, fmt.Errorf("invalid format: missing frontmatter delimiters")
	}

	frontmatter := parts[1]
	body := string(parts[2])

	// 2. Parse Frontmatter
	var def AgentDefinition
	if err := yaml.Unmarshal(frontmatter, &def); err != nil {
		return nil, fmt.Errorf("failed to parse frontmatter: %w", err)
	}

	// 3. Set Prompt
	def.Prompt = strings.TrimSpace(body)

	return &def, nil
}
