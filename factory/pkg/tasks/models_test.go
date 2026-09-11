package tasks

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultModels(t *testing.T) {
	if len(DefaultModels) == 0 {
		t.Error("DefaultModels list should not be empty")
	}

	if DefaultModels[0] != "gemini-3.8-flash" {
		t.Errorf("Expected first default model to be gemini-3.8-flash, got: %s", DefaultModels[0])
	}
}

func TestGetScriptWithDefaults(t *testing.T) {
	// Let's test that GetFixIssueScript successfully replaced the __DEFAULT_MODELS__ placeholder
	scriptBytes, err := GetFixIssueScript()
	if err != nil {
		t.Fatalf("Failed to get fix_issue script: %v", err)
	}

	scriptContent := string(scriptBytes)
	if strings.Contains(scriptContent, "__DEFAULT_MODELS__") {
		t.Error("Expected script to not contain '__DEFAULT_MODELS__' placeholder")
	}

	expectedString := DefaultModelsString()
	if !strings.Contains(scriptContent, expectedString) {
		t.Errorf("Expected script to contain default models list string: %s", expectedString)
	}

	// Test GetRunAgentScript containing runPrecondition function
	runAgentScriptBytes, err := GetRunAgentScript()
	if err != nil {
		t.Fatalf("Failed to get run_agent script: %v", err)
	}
	runAgentScript := string(runAgentScriptBytes)
	if !strings.Contains(runAgentScript, "runPrecondition") {
		t.Error("Expected run_agent script to contain runPrecondition function")
	}
}

func TestRunPrecondition(t *testing.T) {
	runAgentScriptBytes, err := GetRunAgentScript()
	if err != nil {
		t.Fatalf("Failed to get run_agent script: %v", err)
	}
	scriptContent := string(runAgentScriptBytes)

	// Extract runPrecondition function.
	startIdx := strings.Index(scriptContent, "function runPrecondition {")
	if startIdx == -1 {
		t.Fatal("Could not find 'function runPrecondition {' in run_agent.sh")
	}

	endIdx := strings.Index(scriptContent, "}\n\nsetupGit")
	if endIdx == -1 {
		t.Fatal("Could not find the end of runPrecondition function ('}\\n\\nsetupGit')")
	}
	runPreconditionFunc := scriptContent[startIdx : endIdx+1]

	// Portabilize by replacing /workspaces/${REPO_NAME} with temporary test directory
	runPreconditionFunc = strings.ReplaceAll(runPreconditionFunc, `"/workspaces/${REPO_NAME}"`, `"$TEST_WORK_DIR"`)

	tests := []struct {
		name               string
		scriptBody         string
		expectedLogs       []string
		expectedOutput     string // check in agent-output.txt if non-empty
		expectOutputExists bool
	}{
		{
			name: "Python script, pass",
			scriptBody: `import sys
print("Running Python precondition check")
sys.exit(0)
`,
			expectedLogs: []string{
				"Precondition compiles as Python; running with python3.",
				"Running Python precondition check",
				"Precondition script passed.",
			},
			expectOutputExists: false,
		},
		{
			name: "Python script, fail",
			scriptBody: `import sys
print("Running Python precondition check")
sys.exit(5)
`,
			expectedLogs: []string{
				"Precondition compiles as Python; running with python3.",
				"Running Python precondition check",
				"Precondition script failed",
			},
			expectedOutput:     "Deferring workflow.",
			expectOutputExists: true,
		},
		{
			name: "Bash script without shebang, pass",
			scriptBody: `echo "Running Bash precondition check"
exit 0
`,
			expectedLogs: []string{
				"Precondition did not compile as Python; running with bash.",
				"Running Bash precondition check",
				"Precondition script passed.",
			},
			expectOutputExists: false,
		},
		{
			name: "Bash script without shebang, fail",
			scriptBody: `echo "Running Bash precondition check"
exit 3
`,
			expectedLogs: []string{
				"Precondition did not compile as Python; running with bash.",
				"Running Bash precondition check",
				"Precondition script failed",
			},
			expectedOutput:     "Deferring workflow.",
			expectOutputExists: true,
		},
		{
			name: "Script with shebang, pass",
			scriptBody: `#!/usr/bin/env bash
echo "Running shebang precondition check"
exit 0
`,
			expectedLogs: []string{
				"Precondition has shebang; running directly.",
				"Running shebang precondition check",
				"Precondition script passed.",
			},
			expectOutputExists: false,
		},
		{
			name: "Script with shebang, fail",
			scriptBody: `#!/usr/bin/env bash
echo "Running shebang precondition check"
exit 2
`,
			expectedLogs: []string{
				"Precondition has shebang; running directly.",
				"Running shebang precondition check",
				"Precondition script failed",
			},
			expectedOutput:     "Deferring workflow.",
			expectOutputExists: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()

			precondFile := filepath.Join(tmpDir, "precond.sh")
			if err := os.WriteFile(precondFile, []byte(tc.scriptBody), 0755); err != nil {
				t.Fatalf("failed to write precond file: %v", err)
			}

			promptFile := filepath.Join(tmpDir, "prompt.txt")
			if err := os.WriteFile(promptFile, []byte("prompt content"), 0644); err != nil {
				t.Fatalf("failed to write prompt file: %v", err)
			}

			// Assemble the full bash script to run the test
			harness := fmt.Sprintf(`#!/bin/bash
%s

export TEST_WORK_DIR="%s"
export PRECONDITION_FILE="%s"
export PROMPT_FILE="%s"
export REPO_NAME="dummy"

runPrecondition
`, runPreconditionFunc, tmpDir, precondFile, promptFile)

			harnessFile := filepath.Join(tmpDir, "harness.sh")
			if err := os.WriteFile(harnessFile, []byte(harness), 0755); err != nil {
				t.Fatalf("failed to write harness script: %v", err)
			}

			cmd := exec.Command("bash", harnessFile)
			outputBytes, err := cmd.CombinedOutput()
			outputStr := string(outputBytes)
			if err != nil {
				t.Fatalf("failed to run harness: %v\nOutput:\n%s", err, outputStr)
			}

			// Verify logs in combined output
			for _, expectedLog := range tc.expectedLogs {
				if !strings.Contains(outputStr, expectedLog) {
					t.Errorf("Expected output log %q was not found in harness execution:\n%s", expectedLog, outputStr)
				}
			}

			// Verify agent-output.txt
			agentOutFile := filepath.Join(tmpDir, "agent-output.txt")
			_, fileErr := os.Stat(agentOutFile)
			if tc.expectOutputExists {
				if os.IsNotExist(fileErr) {
					t.Errorf("Expected agent-output.txt to be created, but it was not")
				} else {
					contentBytes, err := os.ReadFile(agentOutFile)
					if err != nil {
						t.Fatalf("failed to read agent-output.txt: %v", err)
					}
					contentStr := string(contentBytes)
					if !strings.Contains(contentStr, tc.expectedOutput) {
						t.Errorf("Expected agent-output.txt to contain %q, but got %q", tc.expectedOutput, contentStr)
					}
				}
			} else {
				if !os.IsNotExist(fileErr) {
					t.Errorf("Expected agent-output.txt NOT to be created, but it exists")
				}
			}
		})
	}
}
