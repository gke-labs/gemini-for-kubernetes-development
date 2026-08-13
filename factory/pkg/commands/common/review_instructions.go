package common

import (
	"regexp"
	"strings"
)

var (
	reviewHeaderRe = regexp.MustCompile(`(?i)^(#{1,6})\s+Review\s+Instructions\s*$`)
	listPrefixRe   = regexp.MustCompile(`^(?:[-*]|\d+\.)\s+`)
)

// ExtractReviewInstructions parses one or more markdown bodies (e.g. PR description, parent Issue body)
// and returns all lines under a "#/## Review Instructions" section up until the next heading of equal or higher level.
func ExtractReviewInstructions(bodies ...string) []string {
	for _, body := range bodies {
		instructions := parseReviewInstructionsSection(body)
		if len(instructions) > 0 {
			return instructions
		}
	}
	return nil
}

func parseReviewInstructionsSection(body string) []string {
	if strings.TrimSpace(body) == "" {
		return nil
	}

	lines := strings.Split(body, "\n")
	var instructions []string
	inSection := false
	sectionLevel := 0

	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)

		if !inSection {
			if m := reviewHeaderRe.FindStringSubmatch(line); m != nil {
				inSection = true
				sectionLevel = len(m[1]) // number of '#' characters
			}
			continue
		}

		// Check if we hit a heading of equal or higher level (# up to sectionLevel)
		if strings.HasPrefix(line, "#") {
			hashes := 0
			for _, ch := range line {
				if ch == '#' {
					hashes++
				} else {
					break
				}
			}
			// If followed by space and heading level <= sectionLevel, terminate section
			if hashes <= sectionLevel && len(line) > hashes && (line[hashes] == ' ' || line[hashes] == '\t') {
				break
			}
		}

		if line == "" || strings.HasPrefix(line, "<!--") {
			continue
		}

		// Strip list bullet prefixes ('- ', '* ', '1. ')
		cleaned := listPrefixRe.ReplaceAllString(line, "")
		cleaned = strings.TrimSpace(cleaned)

		// Strip enclosing backticks around file paths (`.gemini/...`)
		if strings.HasPrefix(cleaned, "`") && strings.HasSuffix(cleaned, "`") && len(cleaned) >= 2 {
			cleaned = cleaned[1 : len(cleaned)-1]
			cleaned = strings.TrimSpace(cleaned)
		}

		if cleaned != "" {
			instructions = append(instructions, cleaned)
		}
	}

	return instructions
}
