// Package act carries out what the rules engine decides on an item.
package act

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/conventions"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/watch/rules"
)

// WriteClient is what ApplyWrites writes through (github.Client).
type WriteClient interface {
	AddLabels(ctx context.Context, number int, labels []string) error
	RemoveLabel(ctx context.Context, number int, label string) error
	AddAssignees(ctx context.Context, number int, assignees []string) error
	RemoveAssignees(ctx context.Context, number int, assignees []string) error
	AddComment(ctx context.Context, number int, body string) error
}

var _ WriteClient = (*github.Client)(nil)

// Applied is what ApplyWrites wrote: the labels it added and took off,
// for the caller to bring its copy of the item's labels up to date.
type Applied struct {
	Labeled, Unlabeled []string
}

// ApplyWrites carries out a decision's writes (rules.Writes) on issue or
// PR num: the labels, the assignees, then the stop label and the comment.
// A stop's comment names the stop label, which is what PausedAt reads as
// the pause a later give-up count starts from, and how to resume. Dropping
// the item's queued tasks (a stop, or Drop) is the caller's: it holds the
// queue. Each write that fails is logged and the rest go on; dry-run
// prints them instead.
func ApplyWrites(ctx context.Context, gh WriteClient, num int, w rules.Writes, triggerLabel string, dryRun bool) Applied {
	var done Applied
	stopLabel := conventions.StopLabel(triggerLabel)
	labels := w.Label
	if w.Stop {
		labels = append(append([]string(nil), labels...), stopLabel)
	}
	comment := w.Comment
	if w.Stop {
		comment = StopComment(w.Comment, stopLabel)
	} else if comment != "" {
		comment = "🤖 " + comment
	}
	if dryRun {
		fmt.Printf("[DRYRUN] Would write on #%d: label %v, unlabel %v, assign %v, unassign %v, comment %q\n", num, labels, w.Unlabel, w.Assign, w.Unassign, comment)
		return done
	}
	if len(labels) > 0 {
		if err := gh.AddLabels(ctx, num, labels); err != nil {
			klog.Errorf("Failed to add labels %v to #%d: %v", labels, num, err)
		} else {
			done.Labeled = labels
		}
	}
	for _, l := range w.Unlabel {
		if err := gh.RemoveLabel(ctx, num, l); err != nil {
			klog.Errorf("Failed to remove label %q from #%d: %v", l, num, err)
		} else {
			done.Unlabeled = append(done.Unlabeled, l)
		}
	}
	if len(w.Assign) > 0 {
		if err := gh.AddAssignees(ctx, num, w.Assign); err != nil {
			klog.Errorf("Failed to assign %v to #%d: %v", w.Assign, num, err)
		}
	}
	if len(w.Unassign) > 0 {
		if err := gh.RemoveAssignees(ctx, num, w.Unassign); err != nil {
			klog.Errorf("Failed to unassign %v from #%d: %v", w.Unassign, num, err)
		}
	}
	if comment != "" {
		if err := gh.AddComment(ctx, num, comment); err != nil {
			klog.Errorf("Failed to comment on #%d: %v", num, err)
		}
	}
	return done
}

// StopComment is the comment a rule's stop posts: its own words, then the
// stop label it attached and how to resume.
func StopComment(comment, stopLabel string) string {
	if comment = strings.TrimSpace(comment); comment == "" {
		comment = "Pausing automated processing."
	}
	return fmt.Sprintf("🤖 %s I have attached the `%s` label to pause automated processing.\n\nTo try again, remove the `%s` label (and say what to do differently in a comment).", comment, stopLabel, stopLabel)
}
