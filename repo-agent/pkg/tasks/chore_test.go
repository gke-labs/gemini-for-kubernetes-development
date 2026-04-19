// Copyright 2026 The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package tasks

import (
	"bytes"
	"testing"
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
		SkipPR:     true,
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

	expectedCloneURL := `export CLONE_URL="https://github.com/test-owner/test-repo.git"`
	if !bytes.Contains(w.Bytes(), []byte(expectedCloneURL)) {
		t.Errorf("Script does not contain expected CLONE_URL. Got:\n%s", script)
	}

	expectedCloneCmd := `git clone "${CLONE_URL}" "/workspaces/${REPO_NAME}"`
	if !bytes.Contains(w.Bytes(), []byte(expectedCloneCmd)) {
		t.Errorf("Script does not contain expected git clone command. Got:\n%s", script)
	}

	expectedRunGemini := `function runGemini {`
	if !bytes.Contains(w.Bytes(), []byte(expectedRunGemini)) {
		t.Errorf("Script does not contain expected runGemini function. Got:\n%s", script)
	}

	expectedRestoreConfigDirFiles := `function restoreConfigDirFiles {`
	if !bytes.Contains(w.Bytes(), []byte(expectedRestoreConfigDirFiles)) {
		t.Errorf("Script does not contain expected restoreConfigDirFiles function. Got:\n%s", script)
	}

	expectedCommitChanges := `function commitChanges {`
	if !bytes.Contains(w.Bytes(), []byte(expectedCommitChanges)) {
		t.Errorf("Script does not contain expected commitChanges function. Got:\n%s", script)
	}
}
