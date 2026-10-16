package commands

import (
	"context"
	"testing"
)

// The watch command's flags are the settings that do something: no
// --poll-interval or --labels, and the loop modes.
func TestWatchFlags(t *testing.T) {
	cmd := NewWatchCommand(context.Background())
	for _, gone := range []string{"poll-interval", "labels", "review-mode", "chores-include", "chores-exclude"} {
		if cmd.Flags().Lookup(gone) != nil {
			t.Errorf("--%s is still a flag", gone)
		}
	}
	for _, want := range []string{"issue-mode", "pr-mode", "chores-mode"} {
		if cmd.Flags().Lookup(want) == nil {
			t.Errorf("--%s is not a flag", want)
		}
	}
}

func TestValidMode(t *testing.T) {
	for m, want := range map[string]bool{"enabled": true, "disabled": true, "dryrun": true, "": false, "on": false} {
		if got := validMode(m); got != want {
			t.Errorf("validMode(%q) = %v, want %v", m, got, want)
		}
	}
}
