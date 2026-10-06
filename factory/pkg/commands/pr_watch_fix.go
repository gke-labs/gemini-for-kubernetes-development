package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/conventions"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/feedback"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
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
	return awaitAndApply(ctx, sb, gh, sandboxName, id, "post-replies", false)
}

// addressCommentsRevise is the fix recipe's revise that answers the PR's
// feedback, which factory hands it as its feedback input.
const addressCommentsRevise = "address-comments"

// fixFeedbackPolicy is whose feedback on a fix's PR is addressed.
// factory posts as the member, and the member reviews their own PR, so the
// member's words count (the PR's author, and the token's login, are both
// theirs); what factory posted from a task output does not
// (factoryPosted), and neither do bots.
var fixFeedbackPolicy = feedback.Policy{CountPRAuthor: true, Skip: factoryPosted}

// pendingFixFeedback is the feedback on fix PR num not yet addressed:
// conversation comments, review bodies and inline review comments,
// whenever they were said — a commit after one (a Fix CI, say) does not
// answer it — less what carries the member's 👀 (handed to a revise) or
// 👍 (addressed: post-replies), as the overseer's watch reads them
// (conventions.ReactionInterpreter).
func pendingFixFeedback(ctx context.Context, gh *githubv39.Client, owner, repo string, num int) ([]feedback.Item, error) {
	user, _, err := gh.Users.Get(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("resolving the token's GitHub login: %w", err)
	}
	rc := github.ForRepo(gh, owner, repo)
	h, err := feedback.Fetch(ctx, rc, num)
	if err != nil {
		return nil, fmt.Errorf("reading PR #%d's feedback: %w", num, err)
	}
	reactions := conventions.NewReactionInterpreter(rc, user.GetLogin(), nil)
	return feedback.Pending(ctx, &githubv39.PullRequest{Number: &num}, h, fixFeedbackPolicy, reactions, time.Time{}, time.Time{}), nil
}

const (
	// maxFeedbackBody is how much of one comment the feedback input
	// carries; the agent reads the rest on GitHub (its url).
	maxFeedbackBody = 4000
	// maxFeedbackInput bounds the feedback input, which reaches the
	// sandbox as an environment variable. What does not fit stays
	// unacknowledged, for the next round.
	maxFeedbackInput = 64 << 10
)

// feedbackInput is the feedback input of an address-comments revise — a
// JSON list of the items, oldest first as GitHub lists them, each body
// cut at maxFeedbackBody — and the items it holds. None is "".
func feedbackInput(items []feedback.Item) (string, []feedback.Item) {
	var handed []feedback.Item
	data := []byte("")
	for _, it := range items {
		if r := []rune(it.Body); len(r) > maxFeedbackBody {
			it.Body = string(r[:maxFeedbackBody]) + "…"
		}
		next, err := json.Marshal(append(handed, it))
		if err != nil || len(next) > maxFeedbackInput {
			break
		}
		handed, data = append(handed, it), next
	}
	return string(data), handed
}

// handFeedback sets an address-comments revise's feedback input to what
// is pending on the fix's PR (pr_url), and returns the items, for the
// caller to acknowledge once the revise is handed over.
func handFeedback(ctx context.Context, inputs map[string]string, owner, repo string) ([]feedback.Item, *github.Client, error) {
	n, err := strconv.Atoi(inputs["pr_url"][strings.LastIndex(inputs["pr_url"], "/")+1:])
	if err != nil {
		return nil, nil, fmt.Errorf("the fix's PR %q has no number", inputs["pr_url"])
	}
	gh, err := github.NewClient(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("creating github client: %w", err)
	}
	pending, err := pendingFixFeedback(ctx, gh, owner, repo, n)
	if err != nil {
		return nil, nil, err
	}
	var handed []feedback.Item
	inputs["feedback"], handed = feedbackInput(pending)
	return handed, github.ForRepo(gh, owner, repo), nil
}

// factoryPosted is whether a comment is one factory posted from a task
// output (post-replies' replies and reports): not new feedback for a fix
// run to address.
func factoryPosted(body string) bool {
	return strings.Contains(body, "<!-- factory:task-output ")
}
