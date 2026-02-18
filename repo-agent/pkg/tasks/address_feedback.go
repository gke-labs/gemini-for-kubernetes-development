package tasks

import (
	"bytes"
	"fmt"

	reviewv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repowatch/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/github"
)

var _ Task = &AddressFeedbackModel{}

type AddressFeedbackModel struct {
	Repo                  *github.Repository
	PullRequest           *github.PullRequest
	RepositoryCommits     []github.RepositoryCommit
	IssueComments         []github.IssueComment
	OldIssueComments      []github.IssueComment
	PullRequestReviews    []github.PullRequestReview
	OldPullRequestReviews []github.PullRequestReview
	User                  *github.User
	PromptFile            string
	Models                []string
	// Extensions is a list of gemini-cli extensions to install.
	Extensions            []reviewv1alpha1.GeminiExtension
}

func (m *AddressFeedbackModel) Name() string {
	return "address-feedback"
}

func (m *AddressFeedbackModel) PreScript() ([]byte, error) {
	tmpl, err := getScriptTemplate("address_feedback.sh")
	if err != nil {
		return nil, err
	}
	var w bytes.Buffer
	if err := tmpl.Execute(&w, m); err != nil {
		return nil, fmt.Errorf("failed to execute script template: %w", err)
	}
	return w.Bytes(), nil
}

func (m *AddressFeedbackModel) Prompt() ([]byte, error) {
	tmpl, err := getPromptTemplate("address_feedback.txt")
	if err != nil {
		return nil, err
	}
	var w bytes.Buffer
	if err := tmpl.Execute(&w, m); err != nil {
		return nil, fmt.Errorf("failed to execute prompt template: %w", err)
	}
	return w.Bytes(), nil
}

func (m *AddressFeedbackModel) PostScript() ([]byte, error) {
	return nil, nil
}

func (m *AddressFeedbackModel) DraftState() string {
	return "informational"
}
