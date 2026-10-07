package commands

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
)

func TestModeDisabledSafetyChecks(t *testing.T) {
	ctx := context.Background()

	t.Run("fix command with ISSUE_MODE disabled via flag", func(t *testing.T) {
		cmd := NewRootCommand(ctx)
		buf := new(bytes.Buffer)
		cmd.SetOut(buf)
		cmd.SetErr(buf)
		cmd.SetArgs([]string{"fix", "--issue-mode=disabled", "--url", "https://github.com/owner/repo/issues/123"})

		// redirect stdout
		oldStdout := os.Stdout
		r, w, _ := os.Pipe()
		os.Stdout = w

		err := cmd.Execute()

		w.Close()
		os.Stdout = oldStdout

		var outBuf bytes.Buffer
		_, _ = outBuf.ReadFrom(r)
		out := outBuf.String()

		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
		if !strings.Contains(out, "Issue handling is disabled (ISSUE_MODE=disabled)") {
			t.Errorf("expected disabled message, got %q", out)
		}
	})

	t.Run("fix command with ISSUE_MODE disabled via env", func(t *testing.T) {
		t.Setenv("ISSUE_MODE", "disabled")
		cmd := NewRootCommand(ctx)
		buf := new(bytes.Buffer)
		cmd.SetOut(buf)
		cmd.SetErr(buf)
		cmd.SetArgs([]string{"fix", "--url", "https://github.com/owner/repo/issues/123"})

		oldStdout := os.Stdout
		r, w, _ := os.Pipe()
		os.Stdout = w

		err := cmd.Execute()

		w.Close()
		os.Stdout = oldStdout

		var outBuf bytes.Buffer
		_, _ = outBuf.ReadFrom(r)
		out := outBuf.String()

		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
		if !strings.Contains(out, "Issue handling is disabled (ISSUE_MODE=disabled)") {
			t.Errorf("expected disabled message, got %q", out)
		}
	})

	t.Run("issue alias with ISSUE_MODE disabled", func(t *testing.T) {
		t.Setenv("ISSUE_MODE", "disabled")
		cmd := NewRootCommand(ctx)
		cmd.SetArgs([]string{"issue", "--url", "https://github.com/owner/repo/issues/123"})

		oldStdout := os.Stdout
		r, w, _ := os.Pipe()
		os.Stdout = w

		err := cmd.Execute()

		w.Close()
		os.Stdout = oldStdout

		var outBuf bytes.Buffer
		_, _ = outBuf.ReadFrom(r)
		out := outBuf.String()

		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
		if !strings.Contains(out, "Issue handling is disabled (ISSUE_MODE=disabled)") {
			t.Errorf("expected disabled message, got %q", out)
		}
	})

	t.Run("fix command with PR_MODE disabled on non-issue repo URL", func(t *testing.T) {
		t.Setenv("PR_MODE", "disabled")
		cmd := NewRootCommand(ctx)
		cmd.SetArgs([]string{"fix", "--url", "https://github.com/owner/repo", "--name", "task1"})

		oldStdout := os.Stdout
		r, w, _ := os.Pipe()
		os.Stdout = w

		err := cmd.Execute()

		w.Close()
		os.Stdout = oldStdout

		var outBuf bytes.Buffer
		_, _ = outBuf.ReadFrom(r)
		out := outBuf.String()

		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
		if !strings.Contains(out, "PR handling is disabled (PR_MODE=disabled)") {
			t.Errorf("expected disabled message, got %q", out)
		}
	})

	t.Run("pr review command with REVIEW_MODE disabled", func(t *testing.T) {
		t.Setenv("REVIEW_MODE", "disabled")
		cmd := NewRootCommand(ctx)
		cmd.SetArgs([]string{"pr", "review", "--pr-url", "https://github.com/owner/repo/pull/123"})

		oldStdout := os.Stdout
		r, w, _ := os.Pipe()
		os.Stdout = w

		err := cmd.Execute()

		w.Close()
		os.Stdout = oldStdout

		var outBuf bytes.Buffer
		_, _ = outBuf.ReadFrom(r)
		out := outBuf.String()

		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
		if !strings.Contains(out, "PR review is disabled (REVIEW_MODE=disabled)") {
			t.Errorf("expected disabled message, got %q", out)
		}
	})

	t.Run("pr review command with PR_MODE disabled", func(t *testing.T) {
		t.Setenv("PR_MODE", "disabled")
		cmd := NewRootCommand(ctx)
		cmd.SetArgs([]string{"pr", "review", "--pr-url", "https://github.com/owner/repo/pull/123"})

		oldStdout := os.Stdout
		r, w, _ := os.Pipe()
		os.Stdout = w

		err := cmd.Execute()

		w.Close()
		os.Stdout = oldStdout

		var outBuf bytes.Buffer
		_, _ = outBuf.ReadFrom(r)
		out := outBuf.String()

		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
		if !strings.Contains(out, "PR handling is disabled (PR_MODE=disabled)") {
			t.Errorf("expected disabled message, got %q", out)
		}
	})

	t.Run("pr investigate command with PR_MODE disabled", func(t *testing.T) {
		t.Setenv("PR_MODE", "disabled")
		cmd := NewRootCommand(ctx)
		cmd.SetArgs([]string{"pr", "investigate", "--pr-url", "https://github.com/owner/repo/pull/123"})

		oldStdout := os.Stdout
		r, w, _ := os.Pipe()
		os.Stdout = w

		err := cmd.Execute()

		w.Close()
		os.Stdout = oldStdout

		var outBuf bytes.Buffer
		_, _ = outBuf.ReadFrom(r)
		out := outBuf.String()

		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
		if !strings.Contains(out, "PR handling is disabled (PR_MODE=disabled)") {
			t.Errorf("expected disabled message, got %q", out)
		}
	})

	t.Run("pr address-comments command with PR_MODE disabled", func(t *testing.T) {
		t.Setenv("PR_MODE", "disabled")
		cmd := NewRootCommand(ctx)
		cmd.SetArgs([]string{"pr", "address-comments", "--pr-url", "https://github.com/owner/repo/pull/123"})

		oldStdout := os.Stdout
		r, w, _ := os.Pipe()
		os.Stdout = w

		err := cmd.Execute()

		w.Close()
		os.Stdout = oldStdout

		var outBuf bytes.Buffer
		_, _ = outBuf.ReadFrom(r)
		out := outBuf.String()

		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
		if !strings.Contains(out, "PR handling is disabled (PR_MODE=disabled)") {
			t.Errorf("expected disabled message, got %q", out)
		}
	})

	t.Run("pr iterate command with PR_MODE disabled", func(t *testing.T) {
		t.Setenv("PR_MODE", "disabled")
		cmd := NewRootCommand(ctx)
		cmd.SetArgs([]string{"pr", "iterate", "--pr-url", "https://github.com/owner/repo/pull/123"})

		oldStdout := os.Stdout
		r, w, _ := os.Pipe()
		os.Stdout = w

		err := cmd.Execute()

		w.Close()
		os.Stdout = oldStdout

		var outBuf bytes.Buffer
		_, _ = outBuf.ReadFrom(r)
		out := outBuf.String()

		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
		if !strings.Contains(out, "PR handling is disabled (PR_MODE=disabled)") {
			t.Errorf("expected disabled message, got %q", out)
		}
	})

	t.Run("pr watch command with PR_MODE disabled", func(t *testing.T) {
		t.Setenv("PR_MODE", "disabled")
		cmd := NewRootCommand(ctx)
		cmd.SetArgs([]string{"pr", "watch", "--pr-url", "https://github.com/owner/repo/pull/123"})

		oldStdout := os.Stdout
		r, w, _ := os.Pipe()
		os.Stdout = w

		err := cmd.Execute()

		w.Close()
		os.Stdout = oldStdout

		var outBuf bytes.Buffer
		_, _ = outBuf.ReadFrom(r)
		out := outBuf.String()

		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
		if !strings.Contains(out, "PR handling is disabled (PR_MODE=disabled)") {
			t.Errorf("expected disabled message, got %q", out)
		}
	})

	t.Run("pr adopt command with PR_MODE disabled", func(t *testing.T) {
		t.Setenv("PR_MODE", "disabled")
		cmd := NewRootCommand(ctx)
		cmd.SetArgs([]string{"pr", "adopt", "open", "--pr-url", "https://github.com/owner/repo/pull/123"})

		oldStdout := os.Stdout
		r, w, _ := os.Pipe()
		os.Stdout = w

		err := cmd.Execute()

		w.Close()
		os.Stdout = oldStdout

		var outBuf bytes.Buffer
		_, _ = outBuf.ReadFrom(r)
		out := outBuf.String()

		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
		if !strings.Contains(out, "PR handling is disabled (PR_MODE=disabled)") {
			t.Errorf("expected disabled message, got %q", out)
		}
	})

	t.Run("agent create command with CHORES_MODE disabled", func(t *testing.T) {
		t.Setenv("CHORES_MODE", "disabled")
		cmd := NewRootCommand(ctx)
		cmd.SetArgs([]string{"agent", "create", "--url", "https://github.com/owner/repo", "--agent", "test.yaml"})

		oldStdout := os.Stdout
		r, w, _ := os.Pipe()
		os.Stdout = w

		err := cmd.Execute()

		w.Close()
		os.Stdout = oldStdout

		var outBuf bytes.Buffer
		_, _ = outBuf.ReadFrom(r)
		out := outBuf.String()

		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
		if !strings.Contains(out, "Chores handling is disabled (CHORES_MODE=disabled)") {
			t.Errorf("expected disabled message, got %q", out)
		}
	})

	t.Run("chore alias create command with CHORES_MODE disabled", func(t *testing.T) {
		t.Setenv("CHORES_MODE", "disabled")
		cmd := NewRootCommand(ctx)
		cmd.SetArgs([]string{"chore", "create", "--url", "https://github.com/owner/repo", "--agent", "test.yaml"})

		oldStdout := os.Stdout
		r, w, _ := os.Pipe()
		os.Stdout = w

		err := cmd.Execute()

		w.Close()
		os.Stdout = oldStdout

		var outBuf bytes.Buffer
		_, _ = outBuf.ReadFrom(r)
		out := outBuf.String()

		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
		if !strings.Contains(out, "Chores handling is disabled (CHORES_MODE=disabled)") {
			t.Errorf("expected disabled message, got %q", out)
		}
	})
}
