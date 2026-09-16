package conventions

import (
	"testing"

	githubv39 "github.com/google/go-github/v39/github"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
)

func labels(names ...string) []*githubv39.Label {
	out := make([]*githubv39.Label, 0, len(names))
	for _, name := range names {
		out = append(out, &githubv39.Label{Name: githubv39.String(name)})
	}
	return out
}

func TestHasStopLabel(t *testing.T) {
	for _, tc := range []struct {
		name         string
		labels       []*githubv39.Label
		triggerLabel string
		want         bool
	}{
		{name: "overseer default", labels: labels("overseer/stop"), want: true},
		{name: "deployment namespace", labels: labels("mybot/stop"), triggerLabel: "mybot", want: true},
		{
			// A deployment that renamed its trigger label must still honour the
			// stops an operator already applied under the default namespace.
			name:         "overseer default under a custom trigger label",
			labels:       labels("overseer/stop"),
			triggerLabel: "mybot",
			want:         true,
		},
		{name: "another deployment's namespace", labels: labels("otherbot/stop"), triggerLabel: "mybot"},
		{name: "unrelated label", labels: labels("bug"), triggerLabel: "mybot"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := HasStopLabel(tc.labels, tc.triggerLabel); got != tc.want {
				t.Errorf("HasStopLabel(%v, %q) = %v, want %v", tc.labels, tc.triggerLabel, got, tc.want)
			}
		})
	}
}

func TestStopLabel(t *testing.T) {
	if got := StopLabel(""); got != "overseer/stop" {
		t.Errorf("StopLabel(\"\") = %q, want 'overseer/stop'", got)
	}
	if got := StopLabel("mybot"); got != "mybot/stop" {
		t.Errorf("StopLabel(\"mybot\") = %q, want 'mybot/stop'", got)
	}
	// 'overseer' is already the default namespace, so it must not be doubled up.
	if got := StopLabel("overseer"); got != "overseer/stop" {
		t.Errorf("StopLabel(\"overseer\") = %q, want 'overseer/stop'", got)
	}
}

func TestHasTriggerLabel(t *testing.T) {
	if !HasTriggerLabel(labels("factory"), "factory") {
		t.Error("HasTriggerLabel() = false for an issue carrying the trigger label; want true")
	}
	// GitHub compares label names case-insensitively, and so must we.
	if !HasTriggerLabel(labels("Factory"), "factory") {
		t.Error("HasTriggerLabel() = false for a differently cased trigger label; want true")
	}
	if HasTriggerLabel(labels("factory/stop"), "factory") {
		t.Error("HasTriggerLabel() = true for a namespaced label; want false")
	}
}

func TestPriority(t *testing.T) {
	if got := Priority(labels("priority/urgent")); got != api.PriorityUrgent {
		t.Errorf("Priority() = %q, want %q", got, api.PriorityUrgent)
	}
	if got := Priority(nil); got != api.PriorityMedium {
		t.Errorf("Priority(nil) = %q, want %q", got, api.PriorityMedium)
	}
}
