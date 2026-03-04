// Copyright 2026 The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package models

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/google/go-github/v39/github"
)

// DraftReviewComment defines the structure for a review comment with severity
type DraftReviewComment struct {
	Path      string  `yaml:"path" json:"path"`
	Line      *int    `yaml:"line,omitempty" json:"line,omitempty"`
	Position  *int    `yaml:"position,omitempty" json:"position,omitempty"`
	Body      string  `yaml:"body" json:"body"`
	Side      *string `yaml:"side,omitempty" json:"side,omitempty"`
	StartLine *int    `yaml:"start_line,omitempty" json:"start_line,omitempty"`
	Severity  string  `yaml:"severity,omitempty" json:"severity,omitempty"`
}

// UnmarshalJSON implements custom unmarshaling for DraftReviewComment to handle
// line numbers (and other int fields) that might be provided as strings by LLMs.
func (c *DraftReviewComment) UnmarshalJSON(data []byte) error {
	type Alias DraftReviewComment
	aux := &struct {
		Line      json.RawMessage `json:"line"`
		Position  json.RawMessage `json:"position"`
		StartLine json.RawMessage `json:"start_line"`
		*Alias
	}{
		Alias: (*Alias)(c),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	// Handle Line
	if len(aux.Line) > 0 {
		val, err := rawToIntPtr(aux.Line)
		if err != nil {
			return fmt.Errorf("line: %w", err)
		}
		c.Line = val
	}
	// Handle Position
	if len(aux.Position) > 0 {
		val, err := rawToIntPtr(aux.Position)
		if err != nil {
			return fmt.Errorf("position: %w", err)
		}
		c.Position = val
	}
	// Handle StartLine
	if len(aux.StartLine) > 0 {
		val, err := rawToIntPtr(aux.StartLine)
		if err != nil {
			return fmt.Errorf("start_line: %w", err)
		}
		c.StartLine = val
	}

	return nil
}

func rawToIntPtr(raw json.RawMessage) (*int, error) {
	if string(raw) == "null" || len(raw) == 0 {
		return nil, nil
	}

	// Try to unmarshal as int
	var i int
	if err := json.Unmarshal(raw, &i); err == nil {
		return &i, nil
	}

	// Try to unmarshal as string
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if s == "" {
			return nil, nil
		}
		i, err := strconv.Atoi(s)
		if err != nil {
			return nil, err
		}
		return &i, nil
	}

	return nil, fmt.Errorf("failed to decode as int or string: %s", string(raw))
}

// PullRequestReviewRequest defines the structure for a review request
type PullRequestReviewRequest struct {
	Body     *string               `yaml:"body,omitempty" json:"body,omitempty"`
	Event    *string               `yaml:"event,omitempty" json:"event,omitempty"`
	Comments []*DraftReviewComment `yaml:"comments,omitempty" json:"comments,omitempty"`
}

// RepositoryWatchConfig defines the configuration for watching a repository
type RepositoryWatchConfig struct {
	Owner      string `yaml:"owner" json:"owner"`
	Repo       string `yaml:"repo" json:"repo"`
	Branch     string `yaml:"branch,omitempty" json:"branch,omitempty"`
	ReviewTask bool   `yaml:"review_task,omitempty" json:"review_task,omitempty"`
}

// SandboxTask defines the structure for a task to be executed in a sandbox
type SandboxTask struct {
	ID          string `yaml:"id" json:"id"`
	Type        string `yaml:"type" json:"type"`
	Status      string `yaml:"status" json:"status"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
}

// PullRequestInfo contains information about a Pull Request
type PullRequestInfo struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	Head   string `json:"head"`
	Base   string `json:"base"`
}

// ReviewResult contains the results of an automated review
type ReviewResult struct {
	Comments []*DraftReviewComment `json:"comments"`
}

// IssueInfo contains information about an Issue
type IssueInfo struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
}

// UserConfig contains configuration for a user
type UserConfig struct {
	GithubToken string `json:"github_token"`
}

// PullRequestFile contains information about a file in a Pull Request
type PullRequestFile struct {
	Path      string `json:"path"`
	Patch     string `json:"patch"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Changes   int    `json:"changes"`
}

// PullRequestReview contains information about a Pull Request Review
type PullRequestReview struct {
	ID      int64  `json:"id"`
	User    string `json:"user"`
	Body    string `json:"body"`
	State   string `json:"state"`
	HTMLURL string `json:"html_url"`
}

// ConvertToGithubDraftReviewComments converts DraftReviewComment to github.DraftReviewComment
func ConvertToGithubDraftReviewComments(comments []*DraftReviewComment) []*github.DraftReviewComment {
	var ghComments []*github.DraftReviewComment
	for _, c := range comments {
		ghComments = append(ghComments, &github.DraftReviewComment{
			Path:      &c.Path,
			Line:      c.Line,
			Position:  c.Position,
			Body:      &c.Body,
			Side:      c.Side,
			StartLine: c.StartLine,
		})
	}
	return ghComments
}
