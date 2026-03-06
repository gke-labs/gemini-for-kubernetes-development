package tasks

import (
	"bytes"
	"testing"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/github"
)

func TestChoreScriptTemplate(t *testing.T) {
	tmpl, err := getScriptTemplate("chore.sh")
	if err != nil {
		t.Fatal(err)
	}

	data := ChoreModel{
		RepoName:   "test-repo",
		RepoOwner:  "test-owner",
		CloneURL:   "https://github.com/test-owner/test-repo.git",
		ChoreName:  "Test Chore",
		ChoreFile:  ".agents/test.md",
		PromptFile: "prompt.txt",
		Metadata: github.TraceabilityMetadata{
			Enabled:          true,
			InstallationName: "test-cluster",
			SandboxTask:      "ns/task",
			Timestamp:        "2026-03-06T12:00:00Z",
		},
	}

	var w bytes.Buffer
	if err := tmpl.Execute(&w, data); err != nil {
		t.Fatalf("Failed to execute template: %v", err)
	}

	script := w.String()

	expectedRepoName := `export REPO_NAME="test-repo"`
	if !bytes.Contains(w.Bytes(), []byte(expectedRepoName)) {
		t.Errorf("Script does not contain expected REPO_NAME. Got:\n%s", script)
	}

	expectedMetadata := `export INSTALLATION_NAME="test-cluster"`
	if !bytes.Contains(w.Bytes(), []byte(expectedMetadata)) {
		t.Errorf("Script does not contain expected metadata. Got:\n%s", script)
	}

	expectedTimestamp := `export METADATA_TIMESTAMP="2026-03-06T12:00:00Z"`
	if !bytes.Contains(w.Bytes(), []byte(expectedTimestamp)) {
		t.Errorf("Script does not contain expected metadata timestamp. Got:\n%s", script)
	}

	expectedCloneURL := `export CLONE_URL="https://github.com/test-owner/test-repo.git"`
	if !bytes.Contains(w.Bytes(), []byte(expectedCloneURL)) {
		t.Errorf("Script does not contain expected CLONE_URL. Got:\n%s", script)
	}

	expectedCloneCmd := `git clone "${CLONE_URL}" "/workspaces/${REPO_NAME}"`
	if !bytes.Contains(w.Bytes(), []byte(expectedCloneCmd)) {
		t.Errorf("Script does not contain expected git clone command. Got:\n%s", script)
	}
}
