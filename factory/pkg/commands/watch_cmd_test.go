package commands

import (
	"context"
	"strings"
	"testing"
)

// The watch command's flags are the settings that do something: no
// --poll-interval or --labels, and a review mode and chore lists beside the
// loop modes.
func TestWatchFlags(t *testing.T) {
	cmd := NewWatchCommand(context.Background())
	for _, gone := range []string{"poll-interval", "labels"} {
		if cmd.Flags().Lookup(gone) != nil {
			t.Errorf("--%s is still a flag", gone)
		}
	}
	for _, want := range []string{"issue-mode", "pr-mode", "review-mode", "chores-mode", "chores-include", "chores-exclude"} {
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

func TestEnvList(t *testing.T) {
	t.Setenv("CHORES_INCLUDE", " triage, release-* ,,")
	if got := strings.Join(envList("CHORES_INCLUDE"), "|"); got != "triage|release-*" {
		t.Errorf("envList = %q", got)
	}
	if got := envList("UNSET_LIST_FOR_TEST"); got != nil {
		t.Errorf("unset envList = %v, want nil", got)
	}
}
