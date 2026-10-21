package watch

import (
	"testing"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/watch/rules"
)

// A task of a rule the scanner still decides is held at start to the facts
// it was decided on, as the preconditions held it: a rebase, an
// investigation or a review to its head; a fix to its issue having no
// linked PR; a comments task holds across a new commit.
func TestStaleHoldsLegacyTasksToTheirFacts(t *testing.T) {
	task := &api.QueueTask{Number: 7, CommitSHA: "aaaaaaaaaaaa"}
	moved := rules.Item{Kind: rules.OnPR, Number: 7, Head: "bbbbbbbbbbbb"}
	same := rules.Item{Kind: rules.OnPR, Number: 7, Head: "aaaaaaaaaaaa"}
	for _, rule := range []string{"conflict", "ci", "review"} {
		if drop, why := stale(rule, task, moved); !drop || why != "head moved aaaaaaa→bbbbbbb" {
			t.Errorf("%s on a moved head: drop %v (%s)", rule, drop, why)
		}
		if drop, why := stale(rule, task, same); drop {
			t.Errorf("%s on its head: dropped (%s)", rule, why)
		}
	}
	if drop, why := stale("comments", task, moved); drop {
		t.Errorf("comments across a new commit: dropped (%s)", why)
	}
	if drop, why := stale("ci", &api.QueueTask{Number: 7}, moved); drop {
		t.Errorf("a task decided at no head: dropped (%s)", why)
	}
	linked := rules.Item{Kind: rules.OnIssue, Number: 12, LinkedPR: "open"}
	if drop, why := stale("fix", &api.QueueTask{Number: 12}, linked); !drop || why != "an open PR is now linked to #12" {
		t.Errorf("a fix with a linked PR: drop %v (%s)", drop, why)
	}
	if drop, why := stale("fix", &api.QueueTask{Number: 12}, rules.Item{Kind: rules.OnIssue, Number: 12}); drop {
		t.Errorf("a fix with no linked PR: dropped (%s)", why)
	}
}
