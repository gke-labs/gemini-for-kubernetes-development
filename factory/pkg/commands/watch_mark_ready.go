package commands

import (
	"context"
	"fmt"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/constants"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// markReadyAs marks pull request pr of owner/repo ready for review as
// login (userRepoClient): the watcher's MarkReadyAs, for a grouped graph's
// PR on login's fork, which the watcher itself may not change.
func markReadyAs(ctx context.Context, owner, repo string, pr int, login string) error {
	gh, err := userRepoClient(ctx, owner, repo, login)
	if err != nil {
		return err
	}
	return gh.MarkPullRequestReady(ctx, pr)
}

// rerunFailedJobsAs reruns the failed jobs of GitHub Actions run run of
// owner/repo as login (userRepoClient): the watcher's RerunFailedJobsAs,
// for a rerun the PR's author deferred, which the watcher may not make.
func rerunFailedJobsAs(ctx context.Context, owner, repo string, run int64, login string) error {
	gh, err := userRepoClient(ctx, owner, repo, login)
	if err != nil {
		return err
	}
	return gh.RerunFailedJobs(ctx, run)
}

// userRepoClient is a client of owner/repo with the GitHub token of
// login's user secret (user-<login>, as factory user onboard writes it).
func userRepoClient(ctx context.Context, owner, repo, login string) (*github.Client, error) {
	kubeClient, err := clients.NewKubernetesClient()
	if err != nil {
		return nil, fmt.Errorf("creating k8s client: %w", err)
	}
	secretName := fmt.Sprintf("user-%s", login)
	secret, err := kubeClient.Clientset.CoreV1().Secrets(rootFlags.Namespace).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("fetching %s's secret %s: %w", login, secretName, err)
	}
	token := string(secret.Data[constants.KeyGithubToken])
	if token == "" {
		return nil, fmt.Errorf("secret %s has no %s", secretName, constants.KeyGithubToken)
	}
	github.NameToken(token, login)
	return github.ForRepo(github.NewClientWithToken(ctx, token), owner, repo), nil
}
