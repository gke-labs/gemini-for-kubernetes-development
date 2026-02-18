package tasks

import (
	"bytes"
	"fmt"

	reviewv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repowatch/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/github"
)

var _ Task = &InvestigateFailuresModel{}

// FailedRun holds information about a failed workflow run.
type FailedRun struct {
	// ID is the unique identifier of the workflow run.
	ID int64
	// Name is the name of the workflow.
	Name string
	// URL is the web URL for the workflow run.
	URL string
	// HeadSHA is the SHA of the commit the workflow ran on.
	HeadSHA string
	// FailedJobs is a list of jobs that failed within this run.
	FailedJobs []FailedJob
}

// FailedJob holds information about a failed job within a workflow run.
type FailedJob struct {
	// ID is the unique identifier of the job.
	ID int64
	// Name is the name of the job.
	Name string
	// LogPath is the path to the downloaded log file in the sandbox.
	LogPath string
}

// InvestigateFailuresModel is the data model for the investigate-failures task.
type InvestigateFailuresModel struct {
	Repo              *github.Repository
	PullRequest       *github.PullRequest
	RepositoryCommits []github.RepositoryCommit
	User              *github.User
	PromptFile        string
	Models            []string
	FailedRuns        []FailedRun
	// Extensions is a list of gemini-cli extensions to install.
	Extensions        []reviewv1alpha1.GeminiExtension
}

func (m *InvestigateFailuresModel) Name() string {
	return "investigate-failures"
}

func (m *InvestigateFailuresModel) PreScript() ([]byte, error) {
	tmpl, err := getScriptTemplate("investigate_failures.sh")
	if err != nil {
		return nil, err
	}
	var w bytes.Buffer
	if err := tmpl.Execute(&w, m); err != nil {
		return nil, fmt.Errorf("failed to execute script template: %w", err)
	}
	return w.Bytes(), nil
}

func (m *InvestigateFailuresModel) Prompt() ([]byte, error) {
	tmpl, err := getPromptTemplate("investigate_failures.txt")
	if err != nil {
		return nil, err
	}
	var w bytes.Buffer
	if err := tmpl.Execute(&w, m); err != nil {
		return nil, fmt.Errorf("failed to execute prompt template: %w", err)
	}
	return w.Bytes(), nil
}

func (m *InvestigateFailuresModel) PostScript() ([]byte, error) {
	return nil, nil
}

func (m *InvestigateFailuresModel) DraftState() string {
	return "informational"
}
