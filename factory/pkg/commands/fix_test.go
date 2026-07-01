package commands

import (
	"testing"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/config"
	githubv39 "github.com/google/go-github/v39/github"
)

func stringPtr(s string) *string {
	return &s
}

func TestResolvePRLabels(t *testing.T) {
	tests := []struct {
		name     string
		cfg      *config.FactoryConfig
		issue    *githubv39.Issue
		isIssue  bool
		expected string
	}{
		{
			name:     "Nil config, not an issue",
			cfg:      nil,
			issue:    nil,
			isIssue:  false,
			expected: "factory",
		},
		{
			name: "Config with custom trigger and additional labels, not an issue",
			cfg: &config.FactoryConfig{
				TriggerLabel:     "custom-trigger",
				AdditionalLabels: []string{"label-a", "label-b"},
			},
			issue:    nil,
			isIssue:  false,
			expected: "custom-trigger,label-a,label-b",
		},
		{
			name: "Is an issue, but issue is nil",
			cfg: &config.FactoryConfig{
				TriggerLabel: "factory",
			},
			issue:    nil,
			isIssue:  true,
			expected: "factory",
		},
		{
			name: "Is an issue, inherits all parent labels",
			cfg: &config.FactoryConfig{
				TriggerLabel:     "factory",
				AdditionalLabels: []string{"autogen"},
			},
			issue: &githubv39.Issue{
				Labels: []*githubv39.Label{
					{Name: stringPtr("greenfield")},
					{Name: stringPtr("step/controller")},
				},
			},
			isIssue:  true,
			expected: "factory,autogen,greenfield,step/controller",
		},
		{
			name: "Ignores empty issue label names",
			cfg: &config.FactoryConfig{
				TriggerLabel: "factory",
			},
			issue: &githubv39.Issue{
				Labels: []*githubv39.Label{
					{Name: stringPtr("")},
					{Name: stringPtr("valid-label")},
					{Name: nil},
				},
			},
			isIssue:  true,
			expected: "factory,valid-label",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			actual := resolvePRLabels(tc.cfg, tc.issue, tc.isIssue)
			if actual != tc.expected {
				t.Errorf("expected %q, got %q", tc.expected, actual)
			}
		})
	}
}
