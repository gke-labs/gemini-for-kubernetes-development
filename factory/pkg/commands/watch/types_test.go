package watch

import (
	"testing"

	"github.com/spf13/cobra"
)

func TestRepoFlag(t *testing.T) {
	t.Run("Valid inputs", func(t *testing.T) {
		tests := []struct {
			input     string
			wantOwner string
			wantRepo  string
			wantStr   string
		}{
			{
				input:     "owner/repo",
				wantOwner: "owner",
				wantRepo:  "repo",
				wantStr:   "owner/repo",
			},
			{
				input:     "gke-labs/gemini-for-kubernetes-development",
				wantOwner: "gke-labs",
				wantRepo:  "gemini-for-kubernetes-development",
				wantStr:   "gke-labs/gemini-for-kubernetes-development",
			},
			{
				input:     "kubernetes/kubernetes",
				wantOwner: "kubernetes",
				wantRepo:  "kubernetes",
				wantStr:   "kubernetes/kubernetes",
			},
		}

		for _, tc := range tests {
			var rf RepoFlag
			if err := rf.Set(tc.input); err != nil {
				t.Errorf("rf.Set(%q) unexpected error: %v", tc.input, err)
			}
			if rf.Owner != tc.wantOwner || rf.Repo != tc.wantRepo {
				t.Errorf("rf.Set(%q) = Owner: %q, Repo: %q; want Owner: %q, Repo: %q", tc.input, rf.Owner, rf.Repo, tc.wantOwner, tc.wantRepo)
			}
			if rf.String() != tc.wantStr {
				t.Errorf("rf.String() = %q; want %q", rf.String(), tc.wantStr)
			}
		}
	})

	t.Run("Invalid inputs", func(t *testing.T) {
		invalidInputs := []string{
			"",
			"repo-only",
			"owner/repo/extra",
			"/repo",
			"owner/",
			"/",
			"///",
		}

		for _, input := range invalidInputs {
			var rf RepoFlag
			if err := rf.Set(input); err == nil {
				t.Errorf("rf.Set(%q) expected error, got nil (Owner: %q, Repo: %q)", input, rf.Owner, rf.Repo)
			}
		}
	})

	t.Run("Empty RepoFlag String and Type", func(t *testing.T) {
		var rf RepoFlag
		if rf.String() != "" {
			t.Errorf("empty rf.String() = %q, want empty string", rf.String())
		}
		if rf.Type() != "string" {
			t.Errorf("rf.Type() = %q, want \"string\"", rf.Type())
		}

		var nilRf *RepoFlag
		if nilRf.String() != "" {
			t.Errorf("nilRf.String() = %q, want empty string", nilRf.String())
		}
	})
}

func TestWatchCommand_RepoFlag(t *testing.T) {
	newCmd := func() *cobra.Command {
		var flags Flags
		cmd := &cobra.Command{
			Use: "watch",
			RunE: func(cmd *cobra.Command, args []string) error {
				return nil
			},
		}
		cmd.Flags().Var(&flags.Repo, "repo", "GitHub repository (e.g. owner/repo)")
		_ = cmd.MarkFlagRequired("repo")
		return cmd
	}

	t.Run("Valid repo flag parses into RepoFlag struct", func(t *testing.T) {
		cmd := newCmd()
		cmd.SetArgs([]string{"--repo", "test-org/test-repo"})
		if err := cmd.ParseFlags([]string{"--repo", "test-org/test-repo"}); err != nil {
			t.Fatalf("cmd.ParseFlags failed: %v", err)
		}
		flagVal := cmd.Flag("repo").Value
		if flagVal.String() != "test-org/test-repo" {
			t.Errorf("flagVal.String() = %q, want %q", flagVal.String(), "test-org/test-repo")
		}
	})

	t.Run("Invalid repo flag returns error during flag parsing", func(t *testing.T) {
		cmd := newCmd()
		if err := cmd.ParseFlags([]string{"--repo", "invalid-repo-format"}); err == nil {
			t.Errorf("expected error for invalid repo format, got nil")
		}
	})

	t.Run("Missing repo flag fails required flag validation", func(t *testing.T) {
		cmd := newCmd()
		cmd.SetArgs([]string{})
		err := cmd.Execute()
		if err == nil {
			t.Errorf("expected error for missing repo flag, got nil")
		} else if err.Error() != `required flag(s) "repo" not set` {
			t.Errorf("unexpected error message: %v", err)
		}
	})
}
