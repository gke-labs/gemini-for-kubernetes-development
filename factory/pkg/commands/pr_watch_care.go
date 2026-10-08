package commands

import (
	"context"
	"fmt"
	"strings"
	"time"

	githubv39 "github.com/google/go-github/v39/github"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	factorysandbox "github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/sandbox"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/spool"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/taskapi"
)

// careFollowUp runs one of care's recipes on prURL (care-ci,
// care-comments) — in session care, it continues care's conversation in
// the PR's sandbox, or opens one — waits for it, and posts what it
// answers on the PR (post-replies).
func careFollowUp(ctx context.Context, gh *githubv39.Client, kubeClient *clients.KubernetesClient, prURL, recipeName string) error {
	it, err := parseRecipeTarget(prURL)
	if err != nil {
		return err
	}
	runName := fmt.Sprintf("watch-%s-%d", recipeName, time.Now().Unix())
	if err := runRecipe(ctx, recipeName, prURL, runName, "", false, applyMode{}, map[string]string{}, nil); err != nil {
		return err
	}
	name, err := factorysandbox.PRFixSandbox(ctx, kubeClient, rootFlags.Namespace, it.Repo, it.Number, prURL)
	if err != nil {
		return err
	}
	sb, err := taskapi.Connect(ctx, rootFlags.Namespace, name)
	if err != nil {
		return fmt.Errorf("connecting to sandbox %s: %w", name, err)
	}
	defer sb.Close()
	entries, err := sb.List(ctx)
	if err != nil {
		return err
	}
	e, err := spool.Find(entries, "", runName)
	if err != nil {
		return err
	}
	return awaitAndApply(ctx, sb, gh, nil, name, e.ID, "post-replies", false)
}

// factoryPosted is whether a comment is one factory posted from a task
// output (post-replies' replies and reports): not new feedback for care
// to address.
func factoryPosted(body string) bool {
	return strings.Contains(body, "<!-- factory:task-output ")
}
