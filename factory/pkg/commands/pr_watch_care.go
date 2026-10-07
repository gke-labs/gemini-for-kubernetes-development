package commands

import (
	"context"
	"fmt"
	"strings"
	"time"

	githubv39 "github.com/google/go-github/v39/github"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
	factorysandbox "github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/sandbox"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/spool"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/taskapi"
)

// careRunSandbox is PR prNum's sandbox (recipe-<repo>-<n>) when it records
// a run of the care recipe on the PR, or "". The watch revises that run.
func careRunSandbox(ctx context.Context, kubeClient *clients.KubernetesClient, repo string, prNum int, prURL string) string {
	name := factorysandbox.RecipeSandboxName(repo, prNum, "")
	sb, err := k8s.NewManager(kubeClient).GetSandbox(ctx, rootFlags.Namespace, name)
	if err != nil {
		return ""
	}
	if !careRunOn(sb.GetAnnotations(), prURL) {
		return ""
	}
	return name
}

func careRunOn(annotations map[string]string, prURL string) bool {
	return annotations[factorysandbox.RunAnnotation(careTaskType)] != "" && normalizeItemURL(annotations["htmlURL"]) == normalizeItemURL(prURL)
}

// careTaskType is what the care recipe's runs are recorded under: it
// declares no task-type.
const careTaskType = "recipe-care"

// careFollowUp runs care on prURL for one job — revise (fix-ci,
// address-comments) in care's run in careSandbox, or with none, a start
// focused on it (ci, comments) — waits for it, and posts what it answers
// on the PR (post-replies).
func careFollowUp(ctx context.Context, gh *githubv39.Client, prURL, careSandbox, revise, focus string) error {
	if careSandbox != "" {
		sb, err := taskapi.Connect(ctx, rootFlags.Namespace, careSandbox)
		if err != nil {
			return fmt.Errorf("connecting to sandbox %s: %w", careSandbox, err)
		}
		defer sb.Close()
		id, err := reviseIn(ctx, sb, revise, reviseFlags{recipe: "care"}, nil)
		if err != nil {
			return err
		}
		return awaitAndApply(ctx, sb, gh, nil, careSandbox, id, "post-replies", false)
	}
	it, err := parseRecipeTarget(prURL)
	if err != nil {
		return err
	}
	runName := fmt.Sprintf("watch-care-%s-%d", focus, time.Now().Unix())
	if err := runRecipe(ctx, "care", prURL, runName, "", applyMode{}, map[string]string{"focus": focus}, nil); err != nil {
		return err
	}
	name := factorysandbox.RecipeSandboxName(it.Repo, it.Number, "")
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
