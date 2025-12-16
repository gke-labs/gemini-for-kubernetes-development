package store

import (
	"context"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/review-ui/review-api/pkg/models"
)

type Store interface {
	SaveRepo(ctx context.Context, namespace, name, url string) error
	DeleteRepo(ctx context.Context, namespace, name string) error
	ListRepos(ctx context.Context, namespace string) ([]string, error)

	ListIssues(ctx context.Context, namespace, repo, handler string) ([]models.Issue, error)
	SaveIssue(ctx context.Context, namespace, repo, handler string, issue models.Issue) error
	GetIssue(ctx context.Context, namespace, repo, handler, issueID string) (*models.Issue, error)
	UpdateIssueDraft(ctx context.Context, namespace, repo, handler, issueID, draft string) error
	SaveIssueFeedback(ctx context.Context, owner, repo, handler, issueID, draft, agentDraft, prompt, configdir string) error
	UpdateIssueComment(ctx context.Context, namespace, repo, handler, issueID, comment string) error
	DeleteIssue(ctx context.Context, namespace, repo, handler, issueID string) error

	ListDevSandboxes(ctx context.Context, namespace, repo string) ([]models.DevSandbox, error)
	SaveDevSandbox(ctx context.Context, namespace, repo string, sandbox models.DevSandbox) error
	DeleteDevSandbox(ctx context.Context, namespace, repo, name string) error
}
