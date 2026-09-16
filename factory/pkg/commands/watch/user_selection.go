package watch

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

// selectUserForTask returns the bot account a task should run as, or an empty
// string to fall back to the default factory user.
//
// Selection is the watcher's rather than any one scanner's, because it reads
// the factory role configuration and the cluster: an issue whose sandbox
// already exists is pinned to the account that sandbox belongs to, so that a
// resumed task keeps the credentials and the workspace it started with.
func (w *Watcher) selectUserForTask(ctx context.Context, taskType api.TaskType, prNum int) (string, error) {
	if w.cfg == nil || len(w.cfg.Roles) == 0 {
		return "", nil // default fallback to factory-user
	}

	// 1. Determine role for task type
	role := ""
	for roleName, rCfg := range w.cfg.Roles {
		for _, t := range rCfg.Tasks {
			if strings.EqualFold(t, string(taskType)) {
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
		case taskType == api.TypeAgentChore:
			if rCfg, ok := w.cfg.Roles["agent"]; ok && len(rCfg.Users) > 0 {
				role = "agent"
			} else {
				role = "coder"
			}
		case api.IsPRTask(taskType):
			if prNum > 0 {
				pr, _, err := w.ghClient.PullRequests.Get(ctx, w.Repo.Owner, w.Repo.Repo, prNum)
				if err == nil {
					author := pr.GetUser().GetLogin()
					if author != "" {
						inAgentPool := false
						if agentCfg, ok := w.cfg.Roles["agent"]; ok {
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
		case taskType == api.TypeIssueFix:
			role = "coder"
		case taskType == api.TypePRReview:
			role = "reviewer"
		default:
			return "", nil // default fallback
		}
	}

	roleCfg, exists := w.cfg.Roles[role]
	if !exists || len(roleCfg.Users) == 0 {
		if role == "agent" {
			role = "coder"
			roleCfg, exists = w.cfg.Roles[role]
		}
		if !exists || len(roleCfg.Users) == 0 {
			return "", nil // default fallback
		}
	}

	// 2. Select bot based on new vs existing PR/Issue
	isIssueTask := taskType == api.TypeIssueFix || taskType == api.TypeAgentChore
	if isIssueTask {
		if prNum > 0 {
			// A. First check if a Sandbox already exists for this task on the cluster
			// and has been pinned to a specific user.
			var sandboxName string
			if taskType == api.TypeIssueFix {
				sandboxName = fmt.Sprintf("fix-%s-%d", w.Repo.Repo, prNum)
			} else if taskType == api.TypeAgentChore {
				sandboxName = fmt.Sprintf("wf-issue-%d", prNum)
			}

			if sandboxName != "" && w.kubeClient != nil {
				sb, err := w.kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(w.Namespace).Get(ctx, sandboxName, metav1.GetOptions{})
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
			issue, _, err := w.ghClient.Issues.Get(ctx, w.Repo.Owner, w.Repo.Repo, prNum)
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
		pr, _, err := w.ghClient.PullRequests.Get(ctx, w.Repo.Owner, w.Repo.Repo, prNum)
		if err != nil {
			return "", fmt.Errorf("fetching PR details: %w", err)
		}
		author := pr.GetUser().GetLogin()
		if author == "" {
			return "", fmt.Errorf("empty author login for PR %d", prNum)
		}

		if taskType == api.TypePRReview {
			reviewerRoleCfg, ok := w.cfg.Roles["reviewer"]
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
