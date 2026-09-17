package common

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	githubv39 "github.com/google/go-github/v39/github"
)

// GetReferencedIssues scans a pull request's branch name, title, and body for referenced issue numbers.
func GetReferencedIssues(pr *githubv39.PullRequest) map[int]bool {
	referenced := make(map[int]bool)

	// Check branch name, ignoring epoch timestamps (num >= 10000000)
	if pr.GetHead().GetRef() != "" {
		re := regexp.MustCompile(`\d+`)
		for _, match := range re.FindAllString(pr.GetHead().GetRef(), -1) {
			if num, err := strconv.Atoi(match); err == nil && num < 10000000 {
				referenced[num] = true
			}
		}
	}

	// Check title and body for #1234 or "Fixes/Closes/Resolves/Issue 1234"
	re := regexp.MustCompile(`(?:#|(?i:\b(?:fixes|closes|resolves|issue)\s+))(\d+)\b`)
	for _, text := range []string{pr.GetTitle(), pr.GetBody()} {
		for _, match := range re.FindAllStringSubmatch(text, -1) {
			if len(match) > 1 {
				if num, err := strconv.Atoi(match[1]); err == nil && num < 10000000 {
					referenced[num] = true
				}
			}
		}
	}

	return referenced
}

var (
	// branchIssueRe matches strict branch names like issue-1234, issue_1234, factory-issue-1234, etc.
	branchIssueRe = regexp.MustCompile(`\b(?:issue|factory-issue)[-_](\d+)\b`)

	// closingKwRe matches closing keywords.
	closingKwRe = regexp.MustCompile(`(?i:\b(?:close|closes|closed|fix|fixes|fixed|resolve|resolves|resolved)\b)`)

	// hashIssueRe matches hash references, e.g., #123.
	hashIssueRe = regexp.MustCompile(`#(\d+)\b`)

	// urlIssueRe matches issue URL references, e.g., /issues/123.
	urlIssueRe = regexp.MustCompile(`/issues/(\d+)\b`)
)

// GetClosingIssues scans a pull request's branch name, title, and body for closing references to issue numbers.
func GetClosingIssues(pr *githubv39.PullRequest) map[int]bool {
	closing := make(map[int]bool)

	// Check branch name, ignoring epoch timestamps (num >= 10000000)
	// We restrict this to strict branch formats (e.g. matching branchIssueRe) to avoid false positives.
	if pr.GetHead().GetRef() != "" {
		for _, match := range branchIssueRe.FindAllStringSubmatch(pr.GetHead().GetRef(), -1) {
			if len(match) > 1 {
				if num, err := strconv.Atoi(match[1]); err == nil && num < 10000000 {
					closing[num] = true
				}
			}
		}
	}

	for _, text := range []string{pr.GetTitle(), pr.GetBody()} {
		if text == "" {
			continue
		}

		// Find all occurrences of closing keywords
		matches := closingKwRe.FindAllStringIndex(text, -1)
		for _, match := range matches {
			startIndex := match[1] // right after the keyword

			// Determine the end of the scope in a UTF-8 safe manner
			runes := []rune(text[startIndex:])
			if len(runes) > 150 {
				runes = runes[:150]
			}
			scopeText := string(runes)

			// Truncate at sentence boundaries like period followed by space, semicolon, or newline
			if idx := strings.Index(scopeText, ". "); idx != -1 {
				scopeText = scopeText[:idx]
			}
			if idx := strings.Index(scopeText, ";"); idx != -1 {
				scopeText = scopeText[:idx]
			}
			if idx := strings.Index(scopeText, "\n"); idx != -1 {
				scopeText = scopeText[:idx]
			}

			// Now find all issue numbers inside the scope text
			// e.g. #123 or /issues/123
			for _, hashMatch := range hashIssueRe.FindAllStringSubmatch(scopeText, -1) {
				if len(hashMatch) > 1 {
					if num, err := strconv.Atoi(hashMatch[1]); err == nil && num < 10000000 {
						closing[num] = true
					}
				}
			}

			for _, urlMatch := range urlIssueRe.FindAllStringSubmatch(scopeText, -1) {
				if len(urlMatch) > 1 {
					if num, err := strconv.Atoi(urlMatch[1]); err == nil && num < 10000000 {
						closing[num] = true
					}
				}
			}
		}
	}

	return closing
}

// ExtractRelatedIssuesAndPRs parses the issue body and comments to find all referenced issue and PR numbers.
// It returns a sorted, deduplicated slice of integer issue/PR numbers.
func ExtractRelatedIssuesAndPRs(body string, comments []string, excludeNum int) []int {
	referenced := make(map[int]bool)

	// Combine body and comments for processing
	allTexts := append([]string{body}, comments...)

	// We match patterns like:
	// 1. #123
	// 2. GH-123
	// 3. /issues/123
	// 4. /pull/123 or /pulls/123
	reHash := regexp.MustCompile(`(?:#|(?i:\bgh-))(\d+)\b`)
	reURL := regexp.MustCompile(`\b(?:issues|pull|pulls)/(\d+)\b`)
	reKeyword := regexp.MustCompile(`(?i:\b(?:fixes|closes|resolves|issue|pr)\s+)(\d+)\b`)

	for _, text := range allTexts {
		// Hash / GH- references
		for _, match := range reHash.FindAllStringSubmatch(text, -1) {
			if len(match) > 1 {
				if num, err := strconv.Atoi(match[1]); err == nil && num < 10000000 {
					if num != excludeNum {
						referenced[num] = true
					}
				}
			}
		}
		// URL / path references
		for _, match := range reURL.FindAllStringSubmatch(text, -1) {
			if len(match) > 1 {
				if num, err := strconv.Atoi(match[1]); err == nil && num < 10000000 {
					if num != excludeNum {
						referenced[num] = true
					}
				}
			}
		}
		// Keyword references
		for _, match := range reKeyword.FindAllStringSubmatch(text, -1) {
			if len(match) > 1 {
				if num, err := strconv.Atoi(match[1]); err == nil && num < 10000000 {
					if num != excludeNum {
						referenced[num] = true
					}
				}
			}
		}
	}

	// Convert map keys to a sorted slice
	var result []int
	for num := range referenced {
		result = append(result, num)
	}
	sort.Ints(result)
	return result
}
