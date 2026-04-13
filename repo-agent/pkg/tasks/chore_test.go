package tasks

import (
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/tasks/metadata"
	"bytes"
	"testing"
)

func TestChoreScriptTemplate(t *testing.T) {
	tmpl, err := getScriptTemplate("chore.sh")
	if err != nil {
		t.Fatal(err)
	}

	data := ChoreModel{
		RepoName:                    "test-repo",
		RepoOwner:                   "test-owner",
		CloneURL:                    "https://github.com/test-owner/test-repo.git",
		ChoreName:                   "Test Chore",
		ChoreFile:                   ".agents/test.md",
		PromptFile:                  "prompt.txt",
		SkipPR:                      false,
		TraceabilityMetadataEnabled: true,
		Metadata: metadata.Metadata{
			SandboxTask:    "ns/task",
			SandboxTaskUID: "uid",
			Sandbox:        "sb",
			RepoWatch:      "rw",
			TaskType:       "chore",
			Timestamp:      "2026-03-02T12:00:00Z",
		},
	}

	var w bytes.Buffer
	if err := tmpl.Execute(&w, data); err != nil {
		t.Fatalf("Failed to execute template: %v", err)
	}

	script := w.String()

	missing := []string{}
	for _, expected := range []string{
		`export REPO_NAME="test-repo"`,
		`export CLONE_URL="https://github.com/test-owner/test-repo.git"`,
		`git clone "${CLONE_URL}" "/workspaces/${REPO_NAME}"`,
		"function runGemini {",
		"function restoreConfigDirFiles {",
		"function commitChanges {",
		"sandbox-task: ns/task",
		"sandbox-task-uid: uid",
		"sandbox: sb",
		"repowatch: rw",
		"task-type: chore",
		"timestamp: 2026-03-02T12:00:00Z",
	} {
		if !bytes.Contains(w.Bytes(), []byte(expected)) {
			missing = append(missing, expected)
		}
	}
	if len(missing) > 0 {
		t.Errorf("Script missing %d expected strings: %v\nFull script:\n%s", len(missing), missing, script)
	}
}
