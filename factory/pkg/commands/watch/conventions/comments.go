package conventions

import (
	"context"
	"strings"

	githubv39 "github.com/google/go-github/v39/github"
	"k8s.io/klog/v2"
)

// HasIgnorePrefix reports whether a comment opts itself out of being acted on.
//
// The prefix is "/" + triggerLabel + "-ignore" on any line of the body. The
// '/overseer-ignore' spelling is always accepted, so the directive keeps
// working across deployments that renamed their trigger label.
func HasIgnorePrefix(body string, triggerLabel string) bool {
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.ToLower(strings.TrimSpace(line))
		if strings.HasPrefix(trimmed, "/"+defaultPrefix+"-ignore") {
			return true
		}
		if triggerLabel != "" && !strings.EqualFold(triggerLabel, defaultPrefix) {
			prefix := "/" + strings.ToLower(triggerLabel) + "-ignore"
			if strings.HasPrefix(trimmed, prefix) {
				return true
			}
		}
	}
	return false
}

// CommentResolverClient is the GitHub access ResolveCommentReactions needs: the
// comments on a pull request, the reactions already on them, and the ability to
// add one more.
type CommentResolverClient interface {
	ReactionLister
	// ListIssueComments returns every comment on an issue or pull request.
	ListIssueComments(ctx context.Context, number int) ([]*githubv39.IssueComment, error)
	// AddIssueCommentReaction records a reaction on a conversation comment.
	AddIssueCommentReaction(ctx context.Context, commentID int64, content string) error
}

// ResolveCommentReactions closes out the acknowledgements on a pull request:
// every comment the watcher marked as picked up gets the outcome reaction once
// the task has finished.
//
// It is called from the task lifecycle rather than from a scan, which is why it
// lives here rather than with the scanner that applied the acknowledgement.
//
// Only acknowledged comments are touched. A comment that already carries an
// outcome belongs to an earlier task, and one the watcher never marked was
// never this task's to answer.
func ResolveCommentReactions(ctx context.Context, client CommentResolverClient, prNum int, resolution Reaction, bots []string, selfLogin string) {
	comments, err := client.ListIssueComments(ctx, prNum)
	if err != nil {
		return
	}
	interpreter := NewReactionInterpreter(client, selfLogin, bots)
	for _, c := range comments {
		if ShouldIgnoreUser(c.GetUser(), selfLogin, bots) {
			continue
		}
		if !interpreter.CommentState(ctx, c.GetID()).Acknowledged {
			continue
		}
		if err := client.AddIssueCommentReaction(ctx, c.GetID(), string(resolution)); err != nil {
			klog.Warningf("Failed to resolve reaction on comment %d: %v", c.GetID(), err)
		}
	}
}
