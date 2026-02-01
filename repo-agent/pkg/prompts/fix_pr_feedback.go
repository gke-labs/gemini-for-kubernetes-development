package prompts

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/github"
	githubapi "github.com/google/go-github/v39/github"
	"k8s.io/klog/v2"
	"sigs.k8s.io/yaml"
)

// FixPRFeedbackPrompt generates a prompt to address feedback on a pull request.
// It includes comments and reviews that have not yet been addressed.
// alreadyPostedIDs is a set of comment/review IDs that have already been addressed (or at least already appear in the conversation history).
func FixPRFeedbackPrompt(ctx context.Context, githubAPI *github.Client, repoInfo *github.RepoInfo, pullRequest *github.PullRequest, alreadyPostedIDs map[string]bool) ([]byte, error) {
	log := klog.FromContext(ctx)

	b := ModelBuilder{
		githubAPI:        githubAPI,
		repoInfo:         repoInfo,
		pullRequest:      pullRequest,
		alreadyPostedIDs: alreadyPostedIDs,
	}

	repo := pullRequest.Repo

	pr, _, err := b.githubAPI.PullRequests.Get(ctx, repo.Owner, repo.Name, pullRequest.PullRequestNumber)
	if err != nil {
		return nil, fmt.Errorf("failed to get github pull request: %w", err)
	}
	b.pullRequestInfo = pr

	b.model.PullRequest = PullRequest{
		URL:    pr.GetHTMLURL(),
		Number: pr.GetNumber(),
		Title:  pr.GetTitle(),
		Body:   pr.GetBody(),
	}

	b.model.Upstream = repoInfo.GitCloneURL()
	b.model.DefaultBranch = repoInfo.DefaultBranch()

	commits, _, err := b.githubAPI.PullRequests.ListCommits(ctx, repo.Owner, repo.Name, pullRequest.PullRequestNumber, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list github pull request commits: %w", err)
	}
	for _, commit := range commits {
		b.model.PullRequest.Commits = append(b.model.PullRequest.Commits, PullRequestCommit{
			SHA:     commit.GetSHA(),
			Message: commit.GetCommit().GetMessage(),
		})
	}

	issueCommentListOptions := &githubapi.IssueListCommentsOptions{}
	issueComments, _, err := b.githubAPI.Issues.ListComments(ctx, repo.Owner, repo.Name, pullRequest.PullRequestNumber, issueCommentListOptions)
	if err != nil {
		return nil, fmt.Errorf("failed to list github pull request comments: %w", err)
	}
	for _, comment := range issueComments {
		id := comment.GetNodeID()

		if b.alreadyPostedIDs[id] {
			log.Info("Skipping comment as already posted", "id", id)
			continue
		}

		modelComment := PullRequestComment{
			ID:        id,
			Author:    comment.GetUser().GetLogin(),
			Body:      comment.GetBody(),
			Timestamp: comment.GetCreatedAt(),
		}

		b.model.Comments = append(b.model.Comments, modelComment)
	}

	prCommentListOptions := &githubapi.PullRequestListCommentsOptions{}
	prComments, _, err := b.githubAPI.PullRequests.ListComments(ctx, repo.Owner, repo.Name, pullRequest.PullRequestNumber, prCommentListOptions)
	if err != nil {
		return nil, fmt.Errorf("failed to list github pull request comments: %w", err)
	}
	comentsByPullRequestReviewID := make(map[int64][]*githubapi.PullRequestComment)
	for _, comment := range prComments {
		comentsByPullRequestReviewID[comment.GetPullRequestReviewID()] = append(comentsByPullRequestReviewID[comment.GetPullRequestReviewID()], comment)
	}

	reviewListOptions := &githubapi.ListOptions{PerPage: 100}
	reviews, _, err := githubAPI.PullRequests.ListReviews(ctx, repo.Owner, repo.Name, pullRequest.PullRequestNumber, reviewListOptions)
	if err != nil {
		return nil, fmt.Errorf("failed to list github pull request reviews: %w", err)
	}
	for _, review := range reviews {
		id := review.GetNodeID()

		if alreadyPostedIDs[id] {
			log.Info("Skipping review as already posted", "id", id)
			continue
		}

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

			if out.DiffHunk == "" {
				y, _ := yaml.Marshal(comment)
				log.Info("Empty diff hunk for review comment", "id", id, "comment", string(y))
			}
			modelComment.Reviews = append(modelComment.Reviews, out)
		}

		b.model.Comments = append(b.model.Comments, modelComment)
	}

	if err := b.addTestFailures(ctx); err != nil {
		return nil, err
	}

	sort.Slice(b.model.Comments, func(i, j int) bool {
		return b.model.Comments[i].Timestamp.Before(b.model.Comments[j].Timestamp)
	})

	if len(b.model.Comments) == 0 {
		return nil, nil
	}

	var tmpl *template.Template
	if len(alreadyPostedIDs) > 0 {
		tmpl, err = getTemplate("fix_pr_feedback_incremental.txt")
		if err != nil {
			return nil, err
		}
	} else {
		tmpl, err = getTemplate("fix_pr_feedback.txt")
		if err != nil {
			return nil, err
		}
	}

	var w bytes.Buffer
	if err := tmpl.Execute(&w, &b.model); err != nil {
		return nil, fmt.Errorf("failed to execute prompt template: %w", err)
	}

	return w.Bytes(), nil
}

type FixPRFeedbackPromptModel struct {
	PullRequest PullRequest
	Comments    []PullRequestComment

	Upstream      string
	DefaultBranch string
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

type ModelBuilder struct {
	githubAPI        *github.Client
	repoInfo         *github.RepoInfo
	pullRequest      *github.PullRequest
	pullRequestInfo  *githubapi.PullRequest
	alreadyPostedIDs map[string]bool

	model FixPRFeedbackPromptModel
}

func (b *ModelBuilder) addTestFailures(ctx context.Context) error {
	log := klog.FromContext(ctx)

	repo := b.repoInfo

	{
		listWorkflowRunsOptions := &githubapi.ListWorkflowRunsOptions{
			Branch: b.pullRequestInfo.GetHead().GetRef(),
			// CheckSuite: checkSuite.GetID(),
		}

		runs, _, err := b.githubAPI.Actions.ListRepositoryWorkflowRuns(ctx, repo.Owner, repo.Name, listWorkflowRunsOptions)
		if err != nil {
			return fmt.Errorf("failed to get workflow run for pull request: %w", err)
		}

		for _, run := range runs.WorkflowRuns {

			skip := false
			switch run.GetConclusion() {
			case "success":
				skip = true
			}
			if skip {
				continue
			}

			log.Info("found workflow run", "id", run.GetID(), "name", run.GetName(), "conclusion", run.GetConclusion())

			if run.GetHeadSHA() != b.pullRequestInfo.GetHead().GetSHA() {
				// log.Info("skipping run as head SHA does not match PR", "runHeadSHA", run.GetHeadSHA(), "prHeadSHA", pr.GetHead().GetSHA())
				continue
			}

			runID := run.GetID()

			var allJobs []*githubapi.WorkflowJob
			{
				listWorkflowJobsOptions := &githubapi.ListWorkflowJobsOptions{}
				listWorkflowJobsOptions.Page = 1
				listWorkflowJobsOptions.PerPage = 100
				for {
					jobs, _, err := b.githubAPI.Actions.ListWorkflowJobs(ctx, repo.Owner, repo.Name, runID, listWorkflowJobsOptions)
					if err != nil {
						return fmt.Errorf("failed to list workflow jobs for pull request: %w", err)
					}
					allJobs = append(allJobs, jobs.Jobs...)
					if jobs.GetTotalCount() <= len(allJobs) {
						break
					}
					listWorkflowJobsOptions.Page++
				}
			}

			testFailureCount := 0
			for _, job := range allJobs {
				id := job.GetNodeID()
				if b.alreadyPostedIDs[id] {
					log.Info("Skipping workflow run as already posted", "id", id)
					continue
				}

				if job.GetHeadSHA() != b.pullRequestInfo.GetHead().GetSHA() {
					continue
				}

				skip := false
				switch job.GetConclusion() {
				case "success":
					skip = true
				}
				if skip {
					continue
				}

				body := "Test failed; relevant log lines:\n"

				if job.GetStatus() != "completed" {
					continue
				}

				if job.GetConclusion() == "skipped" {
					log.Info("Skipping job as conclusion is skipped", "name", job.GetName())
					continue
				}

				// Get the logs for this (failed) check
				followRedirects := true
				logsURL, _, err := b.githubAPI.Actions.GetWorkflowJobLogs(ctx, repo.Owner, repo.Name, job.GetID(), followRedirects)
				if err != nil {
					return fmt.Errorf("failed to get workflow run logs for pull request for job %s: %w", job.GetHTMLURL(), err)
				}

				httpClient := http.DefaultClient

				logs, err := httpClient.Get(logsURL.String())
				if err != nil {
					return fmt.Errorf("failed to download workflow run logs for pull request for job %s: %w", job.GetHTMLURL(), err)
				}
				defer logs.Body.Close()
				logsData, err := io.ReadAll(logs.Body)
				if err != nil {
					return fmt.Errorf("failed to read workflow run logs for pull request for job %s: %w", job.GetHTMLURL(), err)
				}

				log.Info("Downloaded logs", "workflowRunName", run.GetName(), "workflowRunID", run.GetID(), "jobID", job.GetID(), "logSize", len(logsData))

				logLines := strings.Split(string(logsData), "\n")
				relevantLines := make(map[int]bool)
				for lineNum, line := range logLines {
					if strings.Contains(line, "FAIL:") {
						relevantLines[lineNum] = true
					}
					if strings.Contains(line, "<hint_for_agent>") {
						relevantLines[lineNum] = true
					}
				}

				// Fallback to looking for ERROR if no "high confidence lines" found
				if len(relevantLines) == 0 {
					for lineNum, line := range logLines {
						if strings.Contains(line, "ERROR") || strings.Contains(line, "Error") || strings.Contains(line, "error") {
							relevantLines[lineNum] = true
						}
					}
				}

				// TODO: What if still no relevant lines?

				for relevantLine := range relevantLines {
					// Include some context lines
					for i := 1; i <= 2; i++ {
						if relevantLine-i >= 0 {
							relevantLines[relevantLine-i] = true
						}
						if relevantLine+i < len(logLines) {
							relevantLines[relevantLine+i] = true
						}
					}
				}

				totalLineCount := 0
				for lineNum, line := range logLines {
					if relevantLines[lineNum] {
						// Add some ... indicators if the lines are not contiguous
						if lineNum > 0 && !relevantLines[lineNum-1] {
							body += "...\n"
						}
						body += fmt.Sprintf("%4d: %s\n", lineNum+1, line)
						totalLineCount++
						if totalLineCount >= 20 {
							body += "...\n"
							break
						}
					}
				}

				modelComment := PullRequestComment{
					Author:    "GitHub Actions Test " + run.GetName() + "/" + job.GetName(),
					Body:      body,
					Timestamp: run.GetUpdatedAt().Time,
					ID:        id,
				}
				b.model.Comments = append(b.model.Comments, modelComment)
				testFailureCount++
				if testFailureCount >= 5 {
					break
				}
			}
		}
	}
	return nil
}
