package commands

import (
	"context"
	"fmt"
	"strings"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
	factorysandbox "github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/sandbox"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/spool"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/taskapi"
)

// fixRunSandbox is the sandbox of the fix recipe's run behind PR prNum
// (design/fix-recipe.md): the one aliased to the PR that records a fix
// run (fix-run), or "". Its PR is looked after by care
// (design/care-recipe.md) instead of `pr investigate` and `pr
// address-comments`.
func fixRunSandbox(ctx context.Context, kubeClient *clients.KubernetesClient, prNum int, prURL string) string {
	list, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(rootFlags.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("factory.gemini.google.com/pr=%d", prNum),
	})
	if err != nil {
		klog.Errorf("Failed to list the sandboxes of PR #%d: %v", prNum, err)
		return ""
	}
	return fixRunOf(list.Items, prURL)
}

func fixRunOf(items []unstructured.Unstructured, prURL string) string {
	for _, sb := range items {
		a := sb.GetAnnotations()
		if a[factorysandbox.RunAnnotation("fix")] != "" && normalizeItemURL(a["htmlURL"]) == normalizeItemURL(prURL) {
			return sb.GetName()
		}
	}
	return ""
}

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

// watchFollowUp is how the watch follows a PR up.
type watchFollowUp int

const (
	// followOverseer: pr investigate and pr address-comments, for a PR no
	// recipe made or looks after.
	followOverseer watchFollowUp = iota
	// followStartCare: start care, for the PR a board fix opened.
	followStartCare
	// followReviseCare: revise care's run on the PR.
	followReviseCare
)

func followUpOf(careSandbox, fixSandbox string) watchFollowUp {
	switch {
	case careSandbox != "":
		return followReviseCare
	case fixSandbox != "":
		return followStartCare
	}
	return followOverseer
}

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
