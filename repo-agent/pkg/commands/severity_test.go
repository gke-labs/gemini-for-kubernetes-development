package commands

import (
	"testing"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/models"
)

func TestGetSeverityLevel(t *testing.T) {
	tests := []struct {
		severity string
		want     int
	}{
		{"CRITICAL", 4},
		{"Critical", 4},
		{"HIGH", 3},
		{"High", 3},
		{"medium", 2},
		{"LOW", 1},
		{"", 2},
		{"unknown", 2},
	}

	for _, tt := range tests {
		if got := getSeverityLevel(tt.severity); got != tt.want {
			t.Errorf("getSeverityLevel(%q) = %v, want %v", tt.severity, got, tt.want)
		}
	}
}

func TestFilterComments(t *testing.T) {
	// Replicate the filtering logic used in ReviewCommand.Run
	filter := func(comments []*models.DraftReviewComment, threshold string) []*models.DraftReviewComment {
		var filtered []*models.DraftReviewComment
		for _, c := range comments {
			if getSeverityLevel(c.Severity) >= getSeverityLevel(threshold) {
				filtered = append(filtered, c)
			}
		}
		return filtered
	}

	critical := "CRITICAL"
	high := "HIGH"
	medium := "MEDIUM"
	low := "LOW"
	empty := ""

	comments := []*models.DraftReviewComment{
		{Severity: critical},
		{Severity: high},
		{Severity: medium},
		{Severity: low},
		{Severity: empty}, // default medium
	}

	tests := []struct {
		threshold string
		want      int
	}{
		{"CRITICAL", 1},
		{"HIGH", 2},
		{"MEDIUM", 4},
		{"LOW", 5},
		{"", 4},
	}

	for _, tt := range tests {
		got := filter(comments, tt.threshold)
		if len(got) != tt.want {
			t.Errorf("filter(..., %q) returned %d comments, want %d", tt.threshold, len(got), tt.want)
		}
	}
}
