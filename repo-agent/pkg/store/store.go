package store

import (
	"context"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/models"
)

type Store interface {
	RequiresPopulate() bool

	SaveRepo(ctx context.Context, namespace, name, url string) error
	DeleteRepo(ctx context.Context, namespace, name string) error
	ListRepos(ctx context.Context, namespace string) ([]string, error)

	ListIssues(ctx context.Context, namespace, repo, handler string) ([]models.Issue, error)
	SaveIssue(ctx context.Context, namespace, repo, handler string, issue models.Issue) error
	GetIssue(ctx context.Context, namespace, repo, handler, issueID string) (*models.Issue, error)
	UpdateIssueDraft(ctx context.Context, namespace, repo, handler, issueID, draft string) error
	SaveIssueFeedback(ctx context.Context, namespace, owner, repo, handler, issueID, draft, agentDraft, prompt, configdir string) error
	UpdateIssueComment(ctx context.Context, namespace, repo, handler, issueID, comment string) error
	DeleteIssue(ctx context.Context, namespace, repo, handler, issueID string) error

	ListDevSandboxes(ctx context.Context, namespace, repo string) ([]models.DevSandbox, error)
	SaveDevSandbox(ctx context.Context, namespace, repo string, sandbox models.DevSandbox) error
	DeleteDevSandbox(ctx context.Context, namespace, repo, name string) error

	ListPRs(ctx context.Context, namespace, repo string) ([]models.PR, error)
	SavePR(ctx context.Context, namespace, repo string, pr models.PR) error
	GetPR(ctx context.Context, namespace, repo, prID string) (*models.PR, error)
	UpdatePRDraft(ctx context.Context, namespace, repo, prID, draft string) error
	UpdatePRReview(ctx context.Context, namespace, repo, prID, review string) error
	SavePRFeedback(ctx context.Context, namespace, owner, repo, prID, draft, agentDraft, prompt, configdir string) error
	DeletePR(ctx context.Context, namespace, repo, prID string) error
}
