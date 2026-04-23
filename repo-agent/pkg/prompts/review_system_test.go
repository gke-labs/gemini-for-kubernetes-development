package prompts

import (
	"strings"
	"testing"

	"github.com/google/go-github/v39/github"
)

func TestExpandReviewPrompt_IgnoreFiles(t *testing.T) {
	model := ReviewPromptModel{
		PullRequest: github.PullRequest{},
		IgnoreFiles: []string{"*.pb.go", "vendor/**"},
	}

	result, err := ExpandReviewPrompt(model)
	if err != nil {
		t.Fatalf("ExpandReviewPrompt failed: %v", err)
	}

	expectedStrs := []string{
		"Do not review files matching the following patterns:",
		"- *.pb.go",
		"- vendor/**",
	}

	for _, str := range expectedStrs {
		if !strings.Contains(result, str) {
			t.Errorf("Result does not contain expected string %q", str)
		}
	}
}

func TestExpandReviewPrompt_NoIgnoreFiles(t *testing.T) {
	model := ReviewPromptModel{
		PullRequest: github.PullRequest{},
		IgnoreFiles: nil,
	}

	result, err := ExpandReviewPrompt(model)
	if err != nil {
		t.Fatalf("ExpandReviewPrompt failed: %v", err)
	}

	unexpectedStr := "Do not review files matching the following patterns:"
	if strings.Contains(result, unexpectedStr) {
		t.Errorf("Result should not contain %q when IgnoreFiles is nil", unexpectedStr)
	}
}

func TestExpandReviewPrompt_IncludeFiles(t *testing.T) {
	model := ReviewPromptModel{
		PullRequest:  github.PullRequest{},
		IncludeFiles: []string{"main.go", "pkg/**"},
	}

	result, err := ExpandReviewPrompt(model)
	if err != nil {
		t.Fatalf("ExpandReviewPrompt failed: %v", err)
	}

	expectedStrs := []string{
		"Only review files matching the following patterns:",
		"- main.go",
		"- pkg/**",
	}

	for _, str := range expectedStrs {
		if !strings.Contains(result, str) {
			t.Errorf("Result does not contain expected string %q", str)
		}
	}
}
