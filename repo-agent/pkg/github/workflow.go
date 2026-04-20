package github

import (
	"context"
	"fmt"

	"github.com/google/go-github/v39/github"
)

type WorkflowRun struct {
	*github.WorkflowRun
}

type WorkflowJob struct {
	*github.WorkflowJob
}

func (c *Client) ListWorkflowRunsByBranch(ctx context.Context, owner, repo, branch string) ([]WorkflowRun, error) {
	opts := &github.ListWorkflowRunsOptions{
		Branch: branch,
		ListOptions: github.ListOptions{
			PerPage: 10,
		}, // Limit to recent runs
	}

	runs, _, err := c.Client.Actions.ListRepositoryWorkflowRuns(ctx, owner, repo, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to list workflow runs: %w", err)
	}

	var workflowRuns []WorkflowRun
	for _, run := range runs.WorkflowRuns {
		workflowRuns = append(workflowRuns, WorkflowRun{run})
	}
	return workflowRuns, nil
}

func (c *Client) ListWorkflowJobs(ctx context.Context, owner, repo string, runID int64) ([]WorkflowJob, error) {
	jobs, _, err := c.Client.Actions.ListWorkflowJobs(ctx, owner, repo, runID, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list workflow jobs: %w", err)
	}

	var workflowJobs []WorkflowJob
	for _, job := range jobs.Jobs {
		workflowJobs = append(workflowJobs, WorkflowJob{job})
	}

	return workflowJobs, nil
}
