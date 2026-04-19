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

package prompts

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/github"
	githubapi "github.com/google/go-github/v39/github"
	"k8s.io/klog/v2"
)

func FixPRFeedbackPrompt(ctx context.Context, githubAPI *github.Client, repo *github.Repo, prNumber int) ([]byte, error) {
	model := FixPRFeedbackPromptModel{}

	pr, _, err := githubAPI.PullRequests.Get(ctx, repo.Owner, repo.Name, prNumber)
	if err != nil {
		return nil, fmt.Errorf("failed to get github pull request: %w", err)
	}

	model.PullRequest = PullRequest{
		URL:    pr.GetHTMLURL(),
		Number: pr.GetNumber(),
		Title:  pr.GetTitle(),
		Body:   pr.GetBody(),
	}

	commits, _, err := githubAPI.PullRequests.ListCommits(ctx, repo.Owner, repo.Name, prNumber, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list github pull request commits: %w", err)
	}
	for _, commit := range commits {
		model.PullRequest.Commits = append(model.PullRequest.Commits, PullRequestCommit{
			SHA:     commit.GetSHA(),
			Message: commit.GetCommit().GetMessage(),
		})
	}

	issueCommentListOptions := &githubapi.IssueListCommentsOptions{}
	issueComments, _, err := githubAPI.Issues.ListComments(ctx, repo.Owner, repo.Name, prNumber, issueCommentListOptions)
	if err != nil {
		return nil, fmt.Errorf("failed to list github pull request comments: %w", err)
	}
	for _, comment := range issueComments {
		id := comment.GetNodeID()

		klog.V(2).Infof("Comment: %+v", comment)
		modelComment := PullRequestComment{
			ID:        id,
			Author:    comment.GetUser().GetLogin(),
			Body:      comment.GetBody(),
			Timestamp: comment.GetCreatedAt(),
		}

		model.Comments = append(model.Comments, modelComment)
	}

	prCommentListOptions := &githubapi.PullRequestListCommentsOptions{}
	prComments, _, err := githubAPI.PullRequests.ListComments(ctx, repo.Owner, repo.Name, prNumber, prCommentListOptions)
	if err != nil {
		return nil, fmt.Errorf("failed to list github pull request comments: %w", err)
	}
	comentsByPullRequestReviewID := make(map[int64][]*githubapi.PullRequestComment)
	for _, comment := range prComments {
		comentsByPullRequestReviewID[comment.GetPullRequestReviewID()] = append(comentsByPullRequestReviewID[comment.GetPullRequestReviewID()], comment)
	}

	reviewListOptions := &githubapi.ListOptions{PerPage: 100}
	reviews, _, err := githubAPI.PullRequests.ListReviews(ctx, repo.Owner, repo.Name, prNumber, reviewListOptions)
	if err != nil {
		return nil, fmt.Errorf("failed to list github pull request reviews: %w", err)
	}
	for _, review := range reviews {
		id := review.GetNodeID()
		modelComment := PullRequestComment{
			ID:        id,
			Author:    review.GetUser().GetLogin(),
			Body:      review.GetBody(),
			Timestamp: review.GetSubmittedAt(),
		}

		comments := comentsByPullRequestReviewID[review.GetID()]

		for _, comment := range comments {
			id := comment.GetNodeID()

			out := PullRequestReview{
				ID:       id,
				Author:   comment.GetUser().GetLogin(),
				Body:     comment.GetBody(),
				Path:     comment.GetPath(),
				DiffHunk: AnnotateDiffHunk(comment.GetDiffHunk()),
			}

			modelComment.Reviews = append(modelComment.Reviews, out)
		}

		model.Comments = append(model.Comments, modelComment)
	}

	sort.Slice(model.Comments, func(i, j int) bool {
		return model.Comments[i].Timestamp.Before(model.Comments[j].Timestamp)
	})

	tmpl, err := getTemplate("fix_pr_feedback.txt")
	if err != nil {
		return nil, err
	}

	var w bytes.Buffer
	if err := tmpl.Execute(&w, &model); err != nil {
		return nil, fmt.Errorf("failed to execute prompt template: %w", err)
	}

	return w.Bytes(), nil
}

type FixPRFeedbackPromptModel struct {
	PullRequest PullRequest
	Comments    []PullRequestComment
}

type PullRequest struct {
	URL     string
	Number  int
	Title   string
	Body    string
	Commits []PullRequestCommit
}

type PullRequestCommit struct {
	SHA     string
	Message string
}

type PullRequestComment struct {
	ID     string
	Author string
	Body   string

	Reviews []PullRequestReview

	Timestamp time.Time
}

type PullRequestReview struct {
	ID       string
	Author   string
	Body     string
	Path     string
	DiffHunk string
}

func AnnotateDiffHunk(diffHunk string) string {
	lines := strings.Split(diffHunk, "\n")
	var annotatedLines []string

	currentOldLine := 0
	currentNewLine := 0
	// Regex to parse the hunk header: @@ -oldStart,oldLen +newStart,newLen @@
	// This is how we get line numbers
	headerRegex := regexp.MustCompile(`^@@ \-(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

	for _, line := range lines {
		if strings.HasPrefix(line, "@@") {
			matches := headerRegex.FindStringSubmatch(line)
			if len(matches) == 3 {
				oldStart, err := strconv.Atoi(matches[1])
				if err == nil {
					currentOldLine = oldStart
				}
				newStart, err := strconv.Atoi(matches[2])
				if err == nil {
					currentNewLine = newStart
				}
			}
			continue
		}

		if currentNewLine == 0 && currentOldLine == 0 {
			// If we haven't seen a header yet, just print the line
			annotatedLines = append(annotatedLines, line)
			continue
		}

		if strings.HasPrefix(line, " ") {
			// Context line
			annotatedLines = append(annotatedLines, fmt.Sprintf("%4d: %s", currentNewLine, line))
			currentOldLine++
			currentNewLine++
		} else if strings.HasPrefix(line, "+") {
			// Added line
			annotatedLines = append(annotatedLines, fmt.Sprintf("%4d: %s", currentNewLine, line))
			currentNewLine++
		} else if strings.HasPrefix(line, "-") {
			// Deleted line - show placeholder for line number
			annotatedLines = append(annotatedLines, fmt.Sprintf("....: %s", line))
			currentOldLine++
		} else {
			// Other (e.g. \ No newline at end of file)
			annotatedLines = append(annotatedLines, fmt.Sprintf(".... : %s", line))
		}
	}

	if len(annotatedLines) > 4 {
		// Only show last 4 lines to avoid too much verbosity - github seems to end the chunk at the "right spot"
		annotatedLines = annotatedLines[len(annotatedLines)-4:]
	}
	return strings.Join(annotatedLines, "\n")
}
