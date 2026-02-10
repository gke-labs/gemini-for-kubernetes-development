package repowatch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/go-github/v39/github"
	reviewv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repowatch/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
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
	isDue, err := r.isAgentTaskDue(ctx, repoWatch, ghClient, owner, repo, agentDef, filename)
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

func (r *Reconciler) isAgentTaskDue(ctx context.Context, repoWatch *reviewv1alpha1.RepoWatch, ghClient *github.Client, owner, repo string, agentDef AgentDefinition, filename string) (bool, error) {
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

	// Try to parse as Go duration first (e.g. "2h", "30m")
	if d, err := time.ParseDuration(schedule); err == nil {
		if time.Since(*lastRun) > d {
			return true, nil
		}
		return false, nil
	}

	// Fallback to LLM for natural language schedules
	return r.isAgentTaskDueWithLLM(ctx, repoWatch, schedule, lastRun)
}

func (r *Reconciler) isAgentTaskDueWithLLM(ctx context.Context, repoWatch *reviewv1alpha1.RepoWatch, schedule string, lastRun *time.Time) (bool, error) {
	log := log.FromContext(ctx)

	// Check cache
	cacheKey := fmt.Sprintf("%s|%s", schedule, lastRun.Format(time.RFC3339))
	if val, ok := r.agentScheduleCache.Load(cacheKey); ok {
		nextRun := val.(time.Time)
		if time.Now().After(nextRun) {
			return true, nil
		}
		return false, nil
	}

	// Call LLM
	apiKey, err := r.getLLMAPIKey(ctx, repoWatch)
	if err != nil {
		log.Error(err, "unable to get LLM API key for agent schedule parsing")
		// Fallback: if we can't parse it, assume not due to avoid spam
		return false, nil
	}

	prompt := fmt.Sprintf(`Given the schedule description "%s" and the last run time "%s" (ISO 8601), what is the expected next run time? Return only the ISO 8601 timestamp for the next run.`, schedule, lastRun.Format(time.RFC3339))

	response, err := r.callGeminiAPI(ctx, apiKey, prompt)
	if err != nil {
		log.Error(err, "LLM call failed for agent schedule")
		return false, nil
	}

	// Clean response (remove quotes, whitespace)
	response = strings.Trim(response, " \"\n\r")

	nextRun, err := time.Parse(time.RFC3339, response)
	if err != nil {
		log.Error(err, "failed to parse LLM response as time", "response", response)
		return false, nil
	}

	r.agentScheduleCache.Store(cacheKey, nextRun)

	if time.Now().After(nextRun) {
		return true, nil
	}
	return false, nil
}

func (r *Reconciler) getLLMAPIKey(ctx context.Context, repoWatch *reviewv1alpha1.RepoWatch) (string, error) {
	var secretName string
	if repoWatch.Spec.Issue != nil && repoWatch.Spec.Issue.LLM.APIKeySecretRef != "" {
		secretName = repoWatch.Spec.Issue.LLM.APIKeySecretRef
	} else if repoWatch.Spec.Review.LLM.APIKeySecretRef != "" {
		secretName = repoWatch.Spec.Review.LLM.APIKeySecretRef
	} else {
		return "", fmt.Errorf("no LLM API key configured in RepoWatch (checked Issue and Review specs)")
	}

	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: secretName, Namespace: repoWatch.Namespace}, secret); err != nil {
		return "", err
	}

	if val, ok := secret.Data["apiKey"]; ok {
		return string(val), nil
	}
	if val, ok := secret.Data["gemini"]; ok {
		return string(val), nil
	}
	return "", fmt.Errorf("secret %s does not contain 'apiKey' or 'gemini' key", secretName)
}

func (r *Reconciler) callGeminiAPI(ctx context.Context, apiKey, prompt string) (string, error) {
	url := "https://generativelanguage.googleapis.com/v1beta/models/gemini-1.5-flash:generateContent?key=" + apiKey

	requestBody := map[string]interface{}{
		"contents": []map[string]interface{}{
			{
				"parts": []map[string]interface{}{
					{
						"text": prompt,
					},
				},
			},
		},
	}

	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, string(body))
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}

	candidates, ok := result["candidates"].([]interface{})
	if !ok || len(candidates) == 0 {
		return "", fmt.Errorf("no candidates in response")
	}

	candidate, ok := candidates[0].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("invalid candidate format")
	}

	content, ok := candidate["content"].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("invalid content format")
	}

	parts, ok := content["parts"].([]interface{})
	if !ok || len(parts) == 0 {
		return "", fmt.Errorf("no parts in content")
	}

	part, ok := parts[0].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("invalid part format")
	}

	text, ok := part["text"].(string)
	if !ok {
		return "", fmt.Errorf("invalid text format")
	}

	return text, nil
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
