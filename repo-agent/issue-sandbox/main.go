package main

import (
	"log"
	"os"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/agentoutput"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/codeserver"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/sandbox"
)

var (
	gvr = schema.GroupVersionResource{
		Group:    "custom.agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "issuesandboxes",
	}
)

func main() {
	go agentoutput.Run("issue", gvr)

	cmdCodeSrv, err := codeserver.Start()
	if err != nil {
		_ = agentoutput.SetAgentState(gvr, "error", err.Error())
		log.Fatalf("failed to start code-server: %v", err)
	}
	defer func() {
		if cmdCodeSrv.Process != nil {
			_ = cmdCodeSrv.Process.Kill()
		}
	}()

	_ = agentoutput.SetAgentState(gvr, "handling issue", "")

	// Create config from env vars
	cfg := sandbox.Config{
		AgentName:        os.Getenv("AGENT_NAME"),
		AgentPrompt:      os.Getenv("AGENT_PROMPT"),
		BranchName:       os.Getenv("ISSUE_BRANCH"),
		PushEnabled:      os.Getenv("GIT_PUSH_ENABLED") == "true",
		GithubUserOrigin: os.Getenv("GITHUB_USER_ORIGIN"),
		GithubUserLogin:  os.Getenv("GITHUB_USER_LOGIN"),
		GithubUserEmail:  os.Getenv("GITHUB_USER_EMAIL"),
		GithubUserName:   os.Getenv("GITHUB_USER_NAME"),
		GVR:              gvr,
		ReportStatus:     true,
	}

	// Prepare git branch
	oldCommitID, err := sandbox.PrepareGitBranch(cfg)
	if err != nil {
		_ = agentoutput.SetAgentState(gvr, "error", err.Error())
		log.Fatalf("failed to prepare git branch: %v", err)
	}

	if _, err := os.Stat("../agent-prompt.txt"); os.IsNotExist(err) {
		// Try solving the issue
		if err := sandbox.RunAgent(cfg); err != nil {
			_ = agentoutput.SetAgentState(gvr, "error", err.Error())
			log.Fatalf("failed solving issue: %v", err)
		}
		_ = agentoutput.SetAgentState(gvr, "done", "")

		// Push the changes
		commitMessage := "fix for issue # " + os.Getenv("ISSUEID")
		if err := sandbox.ProcessGitChanges(cfg, oldCommitID, commitMessage); err != nil {
			_ = agentoutput.SetAgentState(gvr, "error", err.Error())
			log.Fatalf("failed to process git changes: %v", err)
		}
	} else {
		log.Println("agent-prompt.txt exists, skipping code generation")
	}

	// Wait for code-server to exit
	err = cmdCodeSrv.Wait()
	if err != nil {
		log.Printf("Code Server exited with error: %v", err)
	} else {
		log.Println("Code Server exited with no error")
	}
}
