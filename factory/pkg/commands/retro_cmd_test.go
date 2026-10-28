package commands

import (
	"context"
	"io"
	"slices"
	"strings"
	"testing"
	"time"
)

// --since is days, a duration or a date.
func TestParseSince(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	for in, want := range map[string]time.Time{
		"7d":                   time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC),
		"36h":                  time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC),
		"2026-10-20":           time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC),
		"2026-10-20T08:00:00Z": time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC),
	} {
		if got, err := parseSince(in, now); err != nil || !got.Equal(want) {
			t.Errorf("%s: %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "0d", "-3d", "soon"} {
		if _, err := parseSince(bad, now); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

// The flags that need nothing to run: one of --stage-only and --staged,
// and a --staged directory must hold a staged week.
func TestRetroCommandRefuses(t *testing.T) {
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"both":        {[]string{"--stage-only", t.TempDir(), "--staged", t.TempDir()}, "not both"},
		"empty week":  {[]string{"--staged", t.TempDir()}, "holds no staged week"},
		"bad since":   {[]string{"--since", "soon", "--stage-only", t.TempDir()}, "--since"},
		"no repo URL": {[]string{"--url", "https://example.com/o/r", "--stage-only", t.TempDir()}, ""},
	} {
		cmd := NewRetroCommand(context.Background())
		args := tc.args
		if !slices.Contains(args, "--url") {
			args = append([]string{"--url", "https://github.com/o/r"}, args...)
		}
		cmd.SetArgs(args)
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		err := cmd.Execute()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
}
