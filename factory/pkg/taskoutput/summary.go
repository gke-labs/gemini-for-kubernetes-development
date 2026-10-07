package taskoutput

import (
	"fmt"
	"strings"
)

// Summary is a Summary document's spec: what an issue is about, in
// markdown, for whoever comes to it next (comment).
type Summary struct {
	Markdown string `yaml:"markdown"`
}

// SummarySpec decodes a Summary document's spec.
func (d *Document) SummarySpec() (*Summary, error) {
	if d.Kind != "Summary" {
		return nil, fmt.Errorf("%s task output is not a Summary", d.Kind)
	}
	var s Summary
	if err := d.Spec.Decode(&s); err != nil {
		return nil, fmt.Errorf("Summary spec: %w", err)
	}
	if strings.TrimSpace(s.Markdown) == "" {
		return nil, fmt.Errorf("Summary spec has no markdown")
	}
	return &s, nil
}

func parseSummary(raw string) (any, error) {
	md := CleanAgentMarkdown(raw)
	if md == "" {
		return nil, fmt.Errorf("the summary is empty")
	}
	return &Summary{Markdown: md}, nil
}

// SummaryComment is the comment a summary is posted as.
func SummaryComment(s *Summary) string {
	return "**Summary**\n\n" + strings.TrimSpace(s.Markdown)
}
