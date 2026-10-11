package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	githubv39 "github.com/google/go-github/v39/github"
)

// readInstructionFile reads path from the local filesystem, else from the
// repository's default branch.
func readInstructionFile(ctx context.Context, ghClient *githubv39.Client, owner, repo, path string) (string, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		return string(data), nil
	}

	if ghClient != nil {
		fileContent, _, _, err := ghClient.Repositories.GetContents(ctx, owner, repo, path, nil)
		if err == nil && fileContent != nil {
			content, err := fileContent.GetContent()
			if err == nil {
				return content, nil
			}
		}
	}

	return "", fmt.Errorf("instruction file not found locally or in repo: %s", path)
}

// resolveInstruction returns the instruction's text and whether it was a
// file. A value that looks like a path but is found nowhere is an error;
// anything else is the instruction itself.
func resolveInstruction(ctx context.Context, ghClient *githubv39.Client, owner, repo, val string) (string, bool, error) {
	content, err := readInstructionFile(ctx, ghClient, owner, repo, val)
	if err == nil {
		return content, true, nil
	}

	if isLikelyFilePath(val) {
		return "", true, err
	}

	return val, false, nil
}

func isLikelyFilePath(val string) bool {
	val = strings.TrimSpace(val)
	if val == "" {
		return false
	}
	if strings.Contains(val, "\n") || strings.Contains(val, " ") {
		return false
	}
	if strings.HasPrefix(val, "/") || strings.HasPrefix(val, "./") || strings.HasPrefix(val, "../") || strings.HasPrefix(val, "~/") {
		return true
	}
	if strings.Contains(val, "/") || strings.Contains(val, "\\") {
		return true
	}
	return filepath.Ext(val) != ""
}
