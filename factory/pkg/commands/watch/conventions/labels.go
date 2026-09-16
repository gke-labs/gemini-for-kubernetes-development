// Package conventions holds the GitHub vocabulary the watch daemon reads and
// writes: which labels mean what, which accounts count as bots, and how work is
// acknowledged on a comment.
//
// It exists because the subcontrollers must not reach into each other. The
// issue scanner and the pull request scanner both have to recognise a stop
// label, and so does the dispatcher's coordinator; putting that knowledge in a
// leaf package that none of them can import in the other direction is what
// keeps the dependency graph acyclic.
//
// Everything here is a pure function of what GitHub returned. Deciding what to
// do about it belongs to the subcontroller that asked.
package conventions

import (
	"strings"

	githubv39 "github.com/google/go-github/v39/github"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
)

// defaultPrefix is the label namespace used when a deployment has not
// configured a trigger label of its own, and the one every deployment honours
// in addition to its own.
const defaultPrefix = "overseer"

// StopLabel returns the label that pauses automated processing for a given
// trigger label. Both it and the 'overseer/stop' default are recognised by
// HasStopLabel; this is the one the watcher applies when it pauses something
// itself.
func StopLabel(triggerLabel string) string {
	if triggerLabel != "" && !strings.EqualFold(triggerLabel, defaultPrefix) {
		return triggerLabel + "/stop"
	}
	return defaultPrefix + "/stop"
}

// HasStopLabel reports whether an issue or pull request carries a label asking
// the watcher to leave it alone.
//
// The 'overseer/stop' spelling is always honoured, so a deployment that renames
// its trigger label does not silently resume work on everything an operator had
// already stopped.
func HasStopLabel(labels []*githubv39.Label, triggerLabel string) bool {
	return hasAny(labels, namespaced(triggerLabel, "stop"))
}

// HasTriggerLabel reports whether the trigger label itself is present, which is
// what marks an issue as the watcher's to work on.
func HasTriggerLabel(labels []*githubv39.Label, triggerLabel string) bool {
	for _, label := range labels {
		if strings.EqualFold(label.GetName(), triggerLabel) {
			return true
		}
	}
	return false
}

// Priority returns the task priority declared by a 'priority/<level>' label,
// defaulting to medium when an entity declares none.
func Priority(labels []*githubv39.Label) api.TaskPriority {
	for _, l := range labels {
		name := l.GetName()
		if strings.HasPrefix(name, "priority/") {
			return api.TaskPriority(strings.TrimPrefix(name, "priority/"))
		}
	}
	return api.PriorityMedium
}

// namespaced returns the accepted spellings of a label suffix: the
// 'overseer/' default plus the deployment's own trigger label namespace.
func namespaced(triggerLabel, suffix string) []string {
	names := []string{defaultPrefix + "/" + suffix}
	if triggerLabel != "" && !strings.EqualFold(triggerLabel, defaultPrefix) {
		names = append(names, triggerLabel+"/"+suffix)
	}
	return names
}

// hasAny reports whether any of the labels matches one of names, compared
// case-insensitively as GitHub label names are.
func hasAny(labels []*githubv39.Label, names []string) bool {
	for _, label := range labels {
		for _, name := range names {
			if strings.EqualFold(label.GetName(), name) {
				return true
			}
		}
	}
	return false
}
