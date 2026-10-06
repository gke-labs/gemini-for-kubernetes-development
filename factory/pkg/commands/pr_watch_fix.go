package commands

import (
	"context"
	"fmt"
	"strings"

	githubv39 "github.com/google/go-github/v39/github"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
	factorysandbox "github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/sandbox"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/taskapi"
)

// fixRunSandbox is the sandbox of the fix recipe's run behind PR prNum
// (design/fix-recipe.md): the one aliased to the PR that records a fix
// run (fix-run), or "". The watch revises that run instead of running
// `pr investigate` and `pr address-comments`.
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

// reviseFixRun runs one of the fix recipe's revises (fix-ci,
// address-comments) in its sandbox, waits for it, and posts what it
// answers on the PR (post-replies).
func reviseFixRun(ctx context.Context, gh *githubv39.Client, sandboxName, reviseID string) error {
	sb, err := taskapi.Connect(ctx, rootFlags.Namespace, sandboxName)
	if err != nil {
		return fmt.Errorf("connecting to sandbox %s: %w", sandboxName, err)
	}
	defer sb.Close()
	id, err := reviseIn(ctx, sb, reviseID, reviseFlags{}, nil)
	if err != nil {
		return err
	}
	return awaitAndApply(ctx, sb, gh, nil, sandboxName, id, "post-replies", false)
}

// factoryPosted is whether a comment is one factory posted from a task
// output (post-replies' replies and reports): not new feedback for a fix
// run to address.
func factoryPosted(body string) bool {
	return strings.Contains(body, "<!-- factory:task-output ")
}
