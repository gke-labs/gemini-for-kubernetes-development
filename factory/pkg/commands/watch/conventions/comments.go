package conventions

import (
	"context"
	"strings"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
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

// HasIssueCommentReaction reports whether a comment carries the given reaction,
// from a bot when filterBot is set and from a human otherwise.
//
// Reactions are how the watcher records what it has seen: 'eyes' on a comment
// it picked up, '+1' or 'confused' once the task resolved. Distinguishing who
// reacted is the point - a human's 'rocket' asks for the work to be redone,
// whereas the watcher's own marks say it is already handled.
//
// Fetching the reactions is the client's job; deciding whose they are is this
// package's, which is why the predicate lives here rather than beside the call
// that reads them.
func HasIssueCommentReaction(ctx context.Context, client *github.Client, commentID int64, content string, filterBot bool, bots []string, selfLogin string) bool {
	reactions, err := client.IssueCommentReactions(ctx, commentID)
	if err != nil {
		return false
	}
	for _, r := range reactions {
		if r.GetContent() == content {
			isBot := ShouldIgnoreUser(r.GetUser(), selfLogin, bots)
			if filterBot && isBot {
				return true
			} else if !filterBot && !isBot {
				return true
			}
		}
	}
	return false
}

// ResolveCommentReactions closes out the acknowledgements on a pull request:
// every comment the watcher marked with 'eyes' when it picked the work up gets
// the resolution reaction once the task has finished.
//
// It is called from the task lifecycle rather than from a scan, which is why it
// lives here rather than with the scanner that applied the 'eyes'.
func ResolveCommentReactions(ctx context.Context, client *github.Client, prNum int, resolutionContent string, bots []string, selfLogin string) {
	comments, err := client.ListIssueComments(ctx, prNum)
	if err != nil {
		return
	}
	for _, c := range comments {
		if ShouldIgnoreUser(c.GetUser(), selfLogin, bots) {
			continue
		}
		if HasIssueCommentReaction(ctx, client, c.GetID(), "eyes", true, bots, selfLogin) {
			if err := client.AddIssueCommentReaction(ctx, c.GetID(), resolutionContent); err != nil {
				klog.Warningf("Failed to resolve reaction on comment %d: %v", c.GetID(), err)
			}
		}
	}
}
