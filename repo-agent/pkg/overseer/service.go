package overseer

import (
	"context"
	"strings"

	"github.com/google/go-github/v39/github"
)

// Service provides methods to interact with agent definitions in a repository.
type Service struct {
	GHClient *github.Client
}

// NewService creates a new Overseer service.
func NewService(ghClient *github.Client) *Service {
	return &Service{
		GHClient: ghClient,
	}
}

// FetchAgentDefinitions fetches and parses all agent definitions from the .agent/ folder.
func (s *Service) FetchAgentDefinitions(ctx context.Context, owner, repo string) ([]*AgentDefinition, error) {
	_, directoryContent, resp, err := s.GHClient.Repositories.GetContents(ctx, owner, repo, ".agent", nil)
	if err != nil {
		if resp != nil && resp.StatusCode == 404 {
			return nil, nil
		}
		return nil, err
	}

	var definitions []*AgentDefinition
	for _, file := range directoryContent {
		if file.GetType() != "file" || !strings.HasSuffix(file.GetName(), ".md") {
			continue
		}

		fileContent, _, _, err := s.GHClient.Repositories.GetContents(ctx, owner, repo, file.GetPath(), nil)
		if err != nil {
			// Skip files we can't read
			continue
		}

		decoded, err := fileContent.GetContent()
		if err != nil {
			continue
		}

		def, err := ParseAgentDefinition([]byte(decoded))
		if err != nil {
			// Skip invalid definitions
			continue
		}
		definitions = append(definitions, def)
	}
	return definitions, nil
}

// MatchIssue checks if an issue matches any trigger in the agent definition.
func (s *Service) MatchIssue(issue *github.Issue, def *AgentDefinition) bool {
	for _, trigger := range def.Triggers {
		if trigger.Type == "issue" {
			// Check Action if specified
			// Note: GitHub Issue object doesn't have "Action" field directly, it comes from the event payload.
			// But here we are matching against an Issue object, usually from a list.
			// So we might only be able to match state (open/closed) and labels.

			// Check Labels
			if len(trigger.Labels) > 0 {
				hasAllLabels := true
				for _, reqLabel := range trigger.Labels {
					hasLabel := false
					for _, label := range issue.Labels {
						if label.GetName() == reqLabel {
							hasLabel = true
							break
						}
					}
					if !hasLabel {
						hasAllLabels = false
						break
					}
				}
				if !hasAllLabels {
					continue
				}
			}
			return true
		}
	}
	return false
}

// MatchPullRequest checks if a PR matches any trigger in the agent definition.
func (s *Service) MatchPullRequest(pr *github.PullRequest, def *AgentDefinition) bool {
	for _, trigger := range def.Triggers {
		if trigger.Type == "pull_request" {
			// Check Branch
			if trigger.Branch != "" {
				if pr.Base.GetRef() != trigger.Branch {
					continue
				}
			}

			// Check Labels
			if len(trigger.Labels) > 0 {
				hasAllLabels := true
				for _, reqLabel := range trigger.Labels {
					hasLabel := false
					for _, label := range pr.Labels {
						if label.GetName() == reqLabel {
							hasLabel = true
							break
						}
					}
					if !hasLabel {
						hasAllLabels = false
						break
					}
				}
				if !hasAllLabels {
					continue
				}
			}
			return true
		}
	}
	return false
}
