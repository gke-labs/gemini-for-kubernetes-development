package main

import (
	"context"
	"os"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"

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
	ctx := context.Background()
	log := klog.FromContext(ctx)

	go agentoutput.Run("issue", gvr)

	cmdCodeSrv, err := codeserver.Start()
	if err != nil {
		_ = agentoutput.SetAgentState(ctx, gvr, "error", err.Error())
		log.Error(err, "failed to start code-server")
		os.Exit(1)
	}
	defer func() {
		if cmdCodeSrv.Process != nil {
			_ = cmdCodeSrv.Process.Kill()
		}
	}()

	_ = agentoutput.SetAgentState(ctx, gvr, "handling issue", "")

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
		_ = agentoutput.SetAgentState(ctx, gvr, "error", err.Error())
		log.Error(err, "failed to prepare git branch")
		os.Exit(1)
	}

	if _, err := os.Stat("../agent-prompt.txt"); os.IsNotExist(err) {
		// Try solving the issue
		if err := sandbox.RunAgent(ctx, cfg); err != nil {
			_ = agentoutput.SetAgentState(ctx, gvr, "error", err.Error())
			log.Error(err, "failed solving issue")
			os.Exit(1)
		}
		_ = agentoutput.SetAgentState(ctx, gvr, "done", "")

		// Push the changes
		commitMessage := "fix for issue # " + os.Getenv("ISSUEID")
		if err := sandbox.ProcessGitChanges(ctx, cfg, oldCommitID, commitMessage); err != nil {
			_ = agentoutput.SetAgentState(ctx, gvr, "error", err.Error())
			log.Error(err, "failed to process git changes")
			os.Exit(1)
		}
	} else {
		log.Info("agent-prompt.txt exists, skipping code generation")
	}

	// Wait for code-server to exit
	err = cmdCodeSrv.Wait()
	if err != nil {
		log.Error(err, "Code Server exited with error")
	} else {
		log.Info("Code Server exited with no error")
	}
}
