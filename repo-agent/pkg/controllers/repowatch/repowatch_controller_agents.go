package repowatch

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/go-github/v39/github"
	reviewv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repowatch/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/yaml"
)

type AgentDefinition struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Schedule    string `json:"schedule"`
}

func (r *Reconciler) reconcileAgents(ctx context.Context, repoWatch *reviewv1alpha1.RepoWatch, ghClient *github.Client, owner, repo string) error {
	log := log.FromContext(ctx)

	if repoWatch.Spec.Agent == nil || !repoWatch.Spec.Agent.Enabled {
		return nil
	}

	agentPath := repoWatch.Spec.Agent.Path
	if agentPath == "" {
		agentPath = ".agent"
	}

	// List files in the agent directory
	_, directoryContent, _, err := ghClient.Repositories.GetContents(ctx, owner, repo, agentPath, nil)
	if err != nil {
		// If directory not found, just return nil (maybe log info)
		if strings.Contains(err.Error(), "404 Not Found") {
			log.V(4).Info("Agent directory not found", "path", agentPath)
			return nil
		}
		return err
	}

	for _, file := range directoryContent {
		if file.GetType() != "file" {
			continue
		}
		if !strings.HasSuffix(file.GetName(), ".yaml") && !strings.HasSuffix(file.GetName(), ".yml") && !strings.HasSuffix(file.GetName(), ".agent") {
			continue
		}

		content, _, _, err := ghClient.Repositories.GetContents(ctx, owner, repo, file.GetPath(), nil)
		if err != nil {
			log.Error(err, "unable to get agent file content", "file", file.GetPath())
			continue
		}

		decodedContent, err := content.GetContent()
		if err != nil {
			log.Error(err, "unable to decode agent file content", "file", file.GetPath())
			continue
		}

		if err := r.processAgentFile(ctx, repoWatch, ghClient, owner, repo, file.GetName(), decodedContent); err != nil {
			log.Error(err, "unable to process agent file", "file", file.GetPath())
		}
	}

	return nil
}

func (r *Reconciler) processAgentFile(ctx context.Context, repoWatch *reviewv1alpha1.RepoWatch, ghClient *github.Client, owner, repo, filename, content string) error {
	log := log.FromContext(ctx)

	// Parse Frontmatter
	parts := strings.SplitN(content, "---", 3)
	if len(parts) < 3 {
		// Not enough parts for frontmatter, maybe purely yaml? or purely text?
		// Assuming format:
		// ---
		// yaml
		// ---
		// markdown
		return fmt.Errorf("invalid agent file format: expected frontmatter enclosed in '---'")
	}

	frontmatter := parts[1]
	body := parts[2]

	var agentDef AgentDefinition
	if err := yaml.Unmarshal([]byte(frontmatter), &agentDef); err != nil {
		return fmt.Errorf("failed to parse frontmatter: %w", err)
	}

	if agentDef.Name == "" {
		return fmt.Errorf("agent name is required")
	}

	// Check if task is due
	isDue, err := r.isAgentTaskDue(ctx, ghClient, owner, repo, agentDef, filename)
	if err != nil {
		return err
	}

	if isDue {
		log.Info("Creating issue for agent task", "agent", agentDef.Name)
		if err := r.createAgentIssue(ctx, repoWatch, ghClient, owner, repo, agentDef, filename, body); err != nil {
			return err
		}
	}

	return nil
}

func (r *Reconciler) isAgentTaskDue(ctx context.Context, ghClient *github.Client, owner, repo string, agentDef AgentDefinition, filename string) (bool, error) {
	// Check schedule
	// Supports "@weekly", "@daily", or "24h" duration style?
	// Start with strict matching of keywords or duration.

	// Find latest issue with label "agent:<filename>"
	label := fmt.Sprintf("agent:%s", filename)
	query := fmt.Sprintf("repo:%s/%s is:issue label:\"%s\" sort:created-desc", owner, repo, label)

	opts := &github.SearchOptions{
		ListOptions: github.ListOptions{PerPage: 1},
	}
	result, _, err := ghClient.Search.Issues(ctx, query, opts)
	if err != nil {
		return false, err
	}

	if len(result.Issues) == 0 {
		return true, nil // Never run before
	}

	lastRun := result.Issues[0].CreatedAt

	// Parse schedule
	schedule := strings.TrimSpace(agentDef.Schedule)
	var duration time.Duration

	switch schedule {
	case "@weekly":
		duration = 7 * 24 * time.Hour
	case "@daily":
		duration = 24 * time.Hour
	case "@hourly":
		duration = 1 * time.Hour
	default:
		d, err := time.ParseDuration(schedule)
		if err != nil {
			// Treat as cron? No, too complex for now.
			// Just default to never if invalid? Or log error?
			return false, fmt.Errorf("invalid schedule format: %s", schedule)
		}
		duration = d
	}

	if time.Since(*lastRun) > duration {
		return true, nil
	}

	return false, nil
}

func (r *Reconciler) createAgentIssue(ctx context.Context, repoWatch *reviewv1alpha1.RepoWatch, ghClient *github.Client, owner, repo string, agentDef AgentDefinition, filename, body string) error {
	title := fmt.Sprintf("[Agent] %s", agentDef.Name)
	//if agentDef.Description != "" {
	//	// Maybe include description in body?
	//}

	labels := []string{"repo-agent", fmt.Sprintf("agent:%s", filename)}

	request := &github.IssueRequest{
		Title:  &title,
		Body:   &body,
		Labels: &labels,
	}

	_, _, err := ghClient.Issues.Create(ctx, owner, repo, request)
	return err
}
