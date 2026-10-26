package watch

import (
	"context"
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/config"
	githubv39 "github.com/google/go-github/v39/github"
)

func TestWatcher_WhyClosedDisabledByDefault(t *testing.T) {
	w := newTestWatcher(t, t.TempDir())
	w.cfg = &config.FactoryConfig{}
	tracker := w.newWhyClosed()
	if tracker == nil {
		t.Fatal("expected newWhyClosed to return a tracker")
	}

	pr := &githubv39.PullRequest{
		Number: githubv39.Int(1),
		State:  githubv39.String("closed"),
		Merged: githubv39.Bool(false),
		User:   &githubv39.User{Login: githubv39.String("bot")},
	}
	tracker.OnClosedPR(context.Background(), pr)

	if tracker.State().IsAsked(1) {
		t.Error("expected PR not to be asked when AskWhyClosed is disabled")
	}
}

func TestWatcher_WhyClosedEnabled(t *testing.T) {
	w := newTestWatcher(t, t.TempDir())
	w.cfg = &config.FactoryConfig{
		AskWhyClosed: true,
	}
	tracker := w.newWhyClosed()
	if tracker == nil {
		t.Fatal("expected newWhyClosed to return a tracker")
	}

	// Record a reason and check WhyClosedReason
	w.whyClosed = tracker
	tracker.State().RecordAsked(42, "maintainer", time.Now())
	tracker.State().SetCloseReason(42, "superseded", time.Now())

	reason, ok := w.WhyClosedReason(42)
	if !ok || reason != "superseded" {
		t.Errorf("WhyClosedReason(42) = %q, %v; want 'superseded', true", reason, ok)
	}

	_, ok = w.WhyClosedReason(99)
	if ok {
		t.Error("WhyClosedReason(99) should be false for unrecorded PR")
	}
}
