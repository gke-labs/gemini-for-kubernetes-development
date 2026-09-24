package conventions

import (
	"context"
	"strings"

	githubv39 "github.com/google/go-github/v39/github"
	"k8s.io/klog/v2"
)

// Substrings used to identify bot announcements/actions in comments
const (
	AnnouncementStartedAddressingReview   = "started addressing review feedback"
	AnnouncementStartedInvestigatingCheck = "started investigating CI check failures"
	AnnouncementStartedResolvingConflicts = "started resolving merge conflicts"
	AnnouncementStartedReviewing          = "started reviewing"
	AnnouncementStartedFixingIssue        = "started fixing this issue"
	AnnouncementPausingProcessing         = "pausing automated processing"
	AnnouncementPausingInvestigation      = "pausing automated investigation"
)

// DefaultMaxCommentAttempts is the fallback maximum number of comment retry attempts
// when no custom value is configured.
const DefaultMaxCommentAttempts = 3

// Templates for pause messages, defined here to ensure the exact matching substrings are used
const (
	PauseInvestigationTemplate = "🤖 AI Factory has attempted to investigate/fix CI check failures for this pull request %d times since the last commit or update without success. To prevent infinite loops, I am " + AnnouncementPausingInvestigation + " and attaching the `%s` label.\n\nTo request another attempt or resume automated processing, please remove the `%s` label from this pull request (and/or push a new commit or leave a comment)."
	PauseProcessingTemplate    = "🤖 AI Factory has attempted to address review feedback for this pull request %d times since the last commit or update without success. To prevent infinite loops, I am " + AnnouncementPausingProcessing + " and attaching the `%s` label.\n\nTo request another attempt or resume automated processing, please remove the `%s` label from this pull request (and/or push a new commit or leave a comment)."
)

var botAnnouncements = []string{
	AnnouncementStartedAddressingReview,
	AnnouncementStartedInvestigatingCheck,
	AnnouncementStartedResolvingConflicts,
	AnnouncementStartedReviewing,
	AnnouncementStartedFixingIssue,
	AnnouncementPausingProcessing,
	AnnouncementPausingInvestigation,
}

var lowerBotAnnouncements []string

func init() {
	lowerBotAnnouncements = make([]string, len(botAnnouncements))
	for i, a := range botAnnouncements {
		lowerBotAnnouncements[i] = strings.ToLower(a)
	}
}

// IsBotAnnouncement reports whether a comment body is an automated announcement from the bot,
// such as starting a task or pausing processing.
func IsBotAnnouncement(body string) bool {
	if body == "" {
		return false
	}
	lowerBody := strings.ToLower(body)
	for _, a := range lowerBotAnnouncements {
		if strings.Contains(lowerBody, a) {
			return true
		}
	}
	return false
}

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
