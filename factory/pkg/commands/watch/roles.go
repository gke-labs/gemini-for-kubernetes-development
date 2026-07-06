package watch

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/config"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
	githubv39 "github.com/google/go-github/v39/github"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

func selectUserForTask(ctx context.Context, ghClient *githubv39.Client, kubeClient *clients.KubernetesClient, cfg *config.FactoryConfig, taskType string, prNum int, owner, repo string, namespace string) (string, error) {
	if cfg == nil || len(cfg.Roles) == 0 {
		return "", nil // default fallback to factory-user
	}

	// 1. Determine role for task type
	role := ""
	for roleName, rCfg := range cfg.Roles {
		for _, t := range rCfg.Tasks {
			if strings.EqualFold(t, taskType) {
				role = roleName
				break
			}
		}
		if role != "" {
			break
		}
	}

	if role == "" {
		switch {
		case taskType == "agent-chore":
			if rCfg, ok := cfg.Roles["agent"]; ok && len(rCfg.Users) > 0 {
				role = "agent"
			} else {
				role = "coder"
			}
		case isPRTask(taskType):
			if prNum > 0 {
				pr, _, err := ghClient.PullRequests.Get(ctx, owner, repo, prNum)
				if err == nil {
					author := pr.GetUser().GetLogin()
					if author != "" {
						inAgentPool := false
						if agentCfg, ok := cfg.Roles["agent"]; ok {
							for _, u := range agentCfg.Users {
								if strings.EqualFold(u, author) {
									inAgentPool = true
									break
								}
							}
						}
						if inAgentPool {
							role = "agent"
						} else {
							role = "coder"
						}
					}
				}
			}
			if role == "" {
				role = "coder"
			}
		case taskType == "issue-fix":
			role = "coder"
		case taskType == "pr-review":
			role = "reviewer"
		default:
			return "", nil // default fallback
		}
	}

	roleCfg, exists := cfg.Roles[role]
	if !exists || len(roleCfg.Users) == 0 {
		if role == "agent" {
			role = "coder"
			roleCfg, exists = cfg.Roles[role]
		}
		if !exists || len(roleCfg.Users) == 0 {
			return "", nil // default fallback
		}
	}

	// 2. Select bot based on new vs existing PR/Issue
	isIssueTask := taskType == "issue-fix" || taskType == "agent-chore"
	if isIssueTask {
		if prNum > 0 {
			// A. First check if a Sandbox already exists for this task on the cluster
			// and has been pinned to a specific user.
			var sandboxName string
			if taskType == "issue-fix" {
				sandboxName = fmt.Sprintf("fix-%s-%d", repo, prNum)
			} else if taskType == "agent-chore" {
				sandboxName = fmt.Sprintf("wf-issue-%d", prNum)
			}

			if sandboxName != "" && kubeClient != nil {
				sb, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Get(ctx, sandboxName, metav1.GetOptions{})
				if err == nil {
					labels := sb.GetLabels()
					if user, ok := labels["factory.gemini.google.com/user"]; ok && user != "" {
						inPool := false
						for _, u := range roleCfg.Users {
							if strings.EqualFold(u, user) {
								inPool = true
								break
							}
						}
						if inPool {
							klog.Infof("Pinned user '%s' for issue #%d from existing sandbox '%s'", user, prNum, sandboxName)
							return user, nil
						}
					}
				}
			}

			// B. Fallback to GitHub issue assignee check
			issue, _, err := ghClient.Issues.Get(ctx, owner, repo, prNum)
			if err == nil {
				for _, a := range issue.Assignees {
					assignee := a.GetLogin()
					if assignee != "" {
						inPool := false
						for _, u := range roleCfg.Users {
							if strings.EqualFold(u, assignee) {
								inPool = true
								break
							}
						}
						if inPool {
							return assignee, nil
						}
					}
				}
			} else {
				klog.Warningf("Failed to fetch issue details for task %s #%d: %v", taskType, prNum, err)
			}
		}

		idx := time.Now().UnixNano() % int64(len(roleCfg.Users))
		return roleCfg.Users[idx], nil
	}

	if prNum > 0 {
		pr, _, err := ghClient.PullRequests.Get(ctx, owner, repo, prNum)
		if err != nil {
			return "", fmt.Errorf("fetching PR details: %w", err)
		}
		author := pr.GetUser().GetLogin()
		if author == "" {
			return "", fmt.Errorf("empty author login for PR %d", prNum)
		}

		if taskType == "pr-review" {
			reviewerRoleCfg, ok := cfg.Roles["reviewer"]
			if ok && len(reviewerRoleCfg.Users) > 0 {
				idx := time.Now().UnixNano() % int64(len(reviewerRoleCfg.Users))
				return reviewerRoleCfg.Users[idx], nil
			}
			idx := time.Now().UnixNano() % int64(len(roleCfg.Users))
			return roleCfg.Users[idx], nil
		}

		inPool := false
		for _, u := range roleCfg.Users {
			if strings.EqualFold(u, author) {
				inPool = true
				break
			}
		}
		if !inPool {
			return "", fmt.Errorf("PR author '%s' is not in the configured bot pool for role '%s'", author, role)
		}
		return author, nil
	}

	return "", nil
}
