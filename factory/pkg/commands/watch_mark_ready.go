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
// login, with the GitHub token of login's user secret (user-<login>, as
// factory user onboard writes it): the watcher's MarkReadyAs, for a
// grouped graph's PR on login's fork, which the watcher itself may not
// change.
func markReadyAs(ctx context.Context, owner, repo string, pr int, login string) error {
	kubeClient, err := clients.NewKubernetesClient()
	if err != nil {
		return fmt.Errorf("creating k8s client: %w", err)
	}
	secretName := fmt.Sprintf("user-%s", login)
	secret, err := kubeClient.Clientset.CoreV1().Secrets(rootFlags.Namespace).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("fetching %s's secret %s: %w", login, secretName, err)
	}
	token := string(secret.Data[constants.KeyGithubToken])
	if token == "" {
		return fmt.Errorf("secret %s has no %s", secretName, constants.KeyGithubToken)
	}
	github.NameToken(token, login)
	return github.ForRepo(github.NewClientWithToken(ctx, token), owner, repo).MarkPullRequestReady(ctx, pr)
}
