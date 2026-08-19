package watch

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/common"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/config"
	githubv39 "github.com/google/go-github/v39/github"
)

func TestProcessPRs_Filters(t *testing.T) {
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

	stopPRNum := 20
	// Create a dummy pending task file for the stop PR to verify removal
	stopTaskFile := filepath.Join(incomingDir, "task-pr-20-iterate.yaml")
	_ = os.WriteFile(stopTaskFile, []byte("type: pr-iterate\n"), 0644)

	cfg := &config.FactoryConfig{
		MinNumber: 10,
	}

	lowPRNum := 5
	prIssues := []*githubv39.Issue{
		{
			Number: &lowPRNum,
		},
		{
			Number: &stopPRNum,
			Labels: []*githubv39.Label{
				{Name: stringPtr("overseer/stop")},
			},
		},
	}

	processedPRs := make(map[int]prWatchState)
	allBotUsers := []string{"bot1"}

	w.processPRs(context.Background(), nil, nil, cfg, prIssues, processedPRs, allBotUsers, "bot1", incomingDir, processingDir, processedDir, "factory")

	// Verify stop task was removed
	if _, err := os.Stat(stopTaskFile); !os.IsNotExist(err) {
		t.Errorf("expected stop PR task file to be removed, but it still exists")
	}
}

func TestProcessPRs_DisabledMode(t *testing.T) {
	w := &Watcher{
		Flags: Flags{
			PRMode: "disabled",
		},
	}
	// Should return immediately without doing any operations
	w.processPRs(context.Background(), nil, nil, nil, nil, nil, nil, "", "", "", "", "")
}
