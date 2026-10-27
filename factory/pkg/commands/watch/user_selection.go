package watch

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	githubv39 "github.com/google/go-github/v39/github"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/recipe"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/taskoutput"
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
func (w *Watcher) selectUserForTask(ctx context.Context, taskType api.TaskType, recipeName string, prNum int) (string, error) {
	if w.cfg == nil || len(w.cfg.Roles) == 0 {
		return "", nil // default fallback to factory-user
	}

	// 1. The role: what the task's recipe says it runs as - a pool, or
	// author, the pool the PR's author is in - else, for a task with no
	// recipe (a scheduled chore, a nudged workflow step queued before),
	// its type's.
	var role string
	as := w.recipeAs(recipeName)
	switch as {
	case "":
		role = w.roleOfTaskType(ctx, taskType, prNum)
	case recipe.AsAuthor:
		role = w.authorRole(ctx, prNum)
	default:
		role = as
	}
	if role == "" {
		return "", nil // default fallback
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
	isIssueTask := taskType == api.TypeIssueRule || taskType == api.TypeAgentChore
	if isIssueTask {
		if prNum > 0 {
			// A. First check if a Sandbox already exists for this task on the cluster
			// and has been pinned to a specific user.
			var sandboxName string
			if taskType == api.TypeIssueRule {
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

			// B. The author of the issue's comment of the recipe's comment
			// key (its plan, say), when one of the pool: the bot that can
			// edit it. Read only for a recipe with a key, and only when no
			// sandbox pinned the bot.
			if key := w.recipeFacts(recipeName).CommentKey; key != "" {
				if author := w.keyedCommentAuthor(ctx, prNum, key); author != "" && slices.ContainsFunc(roleCfg.Users, func(u string) bool { return strings.EqualFold(u, author) }) {
					klog.Infof("Pinned user '%s' for issue #%d: it wrote the issue's %s comment", author, prNum, key)
					return author, nil
				}
			}

			// C. Fallback to GitHub issue assignee check
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

	// A reviewer is any of the pool: a different identity from the PR's,
	// whoever wrote it.
	if role == "reviewer" {
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

		inPool := false
		for _, u := range roleCfg.Users {
			if strings.EqualFold(u, author) {
				inPool = true
				break
			}
		}
		switch {
		case inPool:
			return author, nil
		case as == recipe.AsAuthor || as == "":
			// The PR's author runs it (it pushes to the PR's branch): one
			// outside the pool cannot.
			return "", fmt.Errorf("%sPR author '%s' is not in the configured bot pool for role '%s'", api.CannotRunPrefix, author, role)
		}
		// A recipe that names a pool (adopt: coder) on someone else's PR:
		// any of the pool.
		idx := time.Now().UnixNano() % int64(len(roleCfg.Users))
		return roleCfg.Users[idx], nil
	}

	return "", nil
}

// roleOfTaskType is the role a task with no recipe runs as: an agent chore
// (a scheduled chore, a workflow's step queued with none) an agent, or a
// coder without an agent pool; "" for any other.
func (w *Watcher) roleOfTaskType(_ context.Context, taskType api.TaskType, _ int) string {
	if taskType != api.TypeAgentChore {
		return ""
	}
	if rCfg, ok := w.cfg.Roles["agent"]; ok && len(rCfg.Users) > 0 {
		return "agent"
	}
	return "coder"
}

// authorRole is the pool the PR's author is in: agent when the agent pool
// has it, else coder.
func (w *Watcher) authorRole(ctx context.Context, prNum int) string {
	if prNum > 0 && w.ghClient != nil {
		pr, _, err := w.ghClient.PullRequests.Get(ctx, w.Repo.Owner, w.Repo.Repo, prNum)
		if err == nil {
			if author := pr.GetUser().GetLogin(); author != "" {
				for _, u := range w.cfg.Roles["agent"].Users {
					if strings.EqualFold(u, author) {
						return "agent"
					}
				}
			}
		}
	}
	return "coder"
}

// recipeAs is who the recipe named runs as (recipe.Recipe.As): the
// repository's recipe of that name when the watch read one (RepoRecipeAs),
// else factory's; "" for no recipe, or one neither has.
func (w *Watcher) recipeAs(name string) string {
	if name == "" {
		return ""
	}
	return w.recipeFacts(name).As
}

// keyedCommentAuthor is who wrote the newest comment on #num of key
// (taskoutput.KeyedComment), or "".
func (w *Watcher) keyedCommentAuthor(ctx context.Context, num int, key string) string {
	if w.ghClient == nil {
		return ""
	}
	var comments []*githubv39.IssueComment
	opts := &githubv39.IssueListCommentsOptions{ListOptions: githubv39.ListOptions{PerPage: 100}}
	for {
		page, resp, err := w.ghClient.Issues.ListComments(ctx, w.Repo.Owner, w.Repo.Repo, num, opts)
		if err != nil {
			klog.Warningf("Reading #%d's comments for its %s comment's author: %v", num, key, err)
			return ""
		}
		comments = append(comments, page...)
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	c, _ := taskoutput.KeyedComment(comments, "", key)
	return c.GetUser().GetLogin()
}

// recipeFacts are the repository's recipe of that name's when the watch
// read one, else factory's; none for no recipe.
func (w *Watcher) recipeFacts(name string) RecipeFacts {
	if name == "" {
		return RecipeFacts{}
	}
	if f, ok := w.repoRecipes[name]; ok {
		return f
	}
	if _, rec, err := recipe.Builtin(name); err == nil {
		return FactsOf(rec)
	}
	return RecipeFacts{}
}

// FactsOf is what the watch reads of rec.
func FactsOf(rec *recipe.Recipe) RecipeFacts {
	f := RecipeFacts{As: rec.As}
	if to := rec.TaskOutput; to != nil && to.Comment != nil {
		f.CommentKey = to.Comment.Key
	}
	return f
}
