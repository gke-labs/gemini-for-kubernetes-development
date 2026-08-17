package watch

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/common"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/config"
	githubv39 "github.com/google/go-github/v39/github"
)

func TestQueueIssueTasks_Filters(t *testing.T) {
	tempDir := t.TempDir()
	incomingDir := filepath.Join(tempDir, "incoming")
	processingDir := filepath.Join(tempDir, "processing")
	processedDir := filepath.Join(tempDir, "processed")
	_ = os.MkdirAll(incomingDir, 0755)
	_ = os.MkdirAll(processingDir, 0755)
	_ = os.MkdirAll(processedDir, 0755)

	w := &Watcher{
		RootFlags: common.RootFlags{
			Namespace: "test-ns",
		},
		Flags: Flags{
			Repo: RepoFlag{
				Owner: "test-owner",
				Repo:  "test-repo",
			},
			QueueDir: tempDir,
		},
	}

	issueNum := 5
	stopIssueNum := 10
	refIssueNum := 15

	// Create a dummy pending task file for the stop issue to verify removal
	stopTaskFile := filepath.Join(incomingDir, "task-issue-10.yaml")
	_ = os.WriteFile(stopTaskFile, []byte("type: issue-fix\n"), 0644)

	cfg := &config.FactoryConfig{
		MinNumber: 6, // issueNum (5) should be skipped
	}

	issues := []*githubv39.Issue{
		{
			Number: &issueNum,
		},
		{
			Number: &stopIssueNum,
			Labels: []*githubv39.Label{
				{Name: stringPtr("overseer/stop")},
			},
		},
		{
			Number: &refIssueNum,
		},
	}

	processedIssues := make(map[int]time.Time)
	refIssues := map[int]bool{
		refIssueNum: true, // Should be skipped
	}

	w.queueIssueTasks(context.Background(), nil, nil, cfg, issues, processedIssues, refIssues, "bot1", []string{"bot1"}, incomingDir, processingDir, processedDir, "factory")

	// Verify stop task was removed
	if _, err := os.Stat(stopTaskFile); !os.IsNotExist(err) {
		t.Errorf("expected stop issue task file to be removed, but it still exists")
	}

	// Verify no new task files created for issue 5, 10, 15
	entries, _ := os.ReadDir(incomingDir)
	if len(entries) != 0 {
		t.Errorf("expected 0 incoming task files, got %d", len(entries))
	}
}
