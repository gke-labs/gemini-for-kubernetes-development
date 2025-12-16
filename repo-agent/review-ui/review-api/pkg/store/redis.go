package store

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/review-ui/review-api/pkg/models"
	redis "github.com/go-redis/redis/v8"
)

// NewClient creates a new Redis client
func NewClient(addr string) *redis.Client {
	return redis.NewClient(&redis.Options{
		Addr: addr,
	})
}

// RedisStore implements the Store interface using Redis
type RedisStore struct {
	client *redis.Client
}

// NewRedisStore creates a new RedisStore
func NewRedisStore(client *redis.Client) *RedisStore {
	return &RedisStore{client: client}
}

func (s *RedisStore) SaveRepo(ctx context.Context, namespace, name, url string) error {
	key := fmt.Sprintf("repo:ns:%s:name:%s", namespace, name)
	return s.client.HSet(ctx, key, "url", url, "namespace", namespace).Err()
}

func (s *RedisStore) DeleteRepo(ctx context.Context, namespace, name string) error {
	key := fmt.Sprintf("repo:ns:%s:name:%s", namespace, name)
	return s.client.Del(ctx, key).Err()
}

func (s *RedisStore) ListRepos(ctx context.Context, namespace string) ([]string, error) {
	prefix := fmt.Sprintf("repo:ns:%s:name:", namespace)
	var repoNames []string
	iter := s.client.Scan(ctx, 0, prefix+"*", 0).Iterator()
	for iter.Next(ctx) {
		key := iter.Val()
		repoName := key[len(prefix):]
		repoNames = append(repoNames, repoName)
	}
	if err := iter.Err(); err != nil {
		return nil, err
	}
	return repoNames, nil
}

func (s *RedisStore) ListIssues(ctx context.Context, namespace, repo, handler string) ([]models.Issue, error) {
	issues := []models.Issue{}
	issueKeyPrefix := fmt.Sprintf("issue:ns:%s:repo:%s:handler:%s:issue:*", namespace, repo, handler)
	iter := s.client.Scan(ctx, 0, issueKeyPrefix, 0).Iterator()
	for iter.Next(ctx) {
		key := iter.Val()
		parts := strings.Split(key, ":")
		// key is issue:repo:REPO:handler:HANDLER:issue:ISSUEID
		// But wait, the prefix used in handlers_issue.go was "issue:ns:%s:repo:%s:handler:%s:issue:*"
		// Let's verify the key structure in handlers_issue.go:
		// key is issue:ns:NAMESPACE:repo:REPO:handler:HANDLER:issue:ISSUEID
		// parts: 0:issue 1:ns 2:NAMESPACE 3:repo 4:REPO 5:handler 6:HANDLER 7:issue 8:ISSUEID
		if len(parts) != 9 {
			continue
		}
		issueID := parts[8]

		issueData, err := s.client.HGetAll(ctx, key).Result()
		if err != nil {
			log.Printf("Failed to get Issue %s from Redis for repo %s handler %s: %v", issueID, repo, handler, err)
			continue
		}

		pushBranch := false
		if val, ok := issueData["pushBranch"]; ok {
			// strconv.ParseBool is needed but I need to import it or just do manual check
			if val == "true" {
				pushBranch = true
			}
		}

		issue := models.Issue{
			ID:         issueID,
			Title:      issueData["title"],
			PushBranch: pushBranch,
		}

		if val, ok := issueData["htmlurl"]; ok {
			issue.HTMLURL = val
		}
		if val, ok := issueData["draft"]; ok {
			issue.Draft = val
		}
		if val, ok := issueData["sandbox"]; ok {
			issue.Sandbox = val
		}
		if val, ok := issueData["sandboxReplica"]; ok {
			issue.SandboxReplica = val
		}
		if val, ok := issueData["comment"]; ok {
			issue.Comment = val
		}
		if val, ok := issueData["branchURL"]; ok {
			issue.BranchURL = val
		}
		// The original handler also had agentDraft in HGetAll but it wasn't put into models.Issue directly,
		// only used for comparison in submitComment. However, ListIssues returns []models.Issue.
		// So this is fine.

		issues = append(issues, issue)
	}
	if err := iter.Err(); err != nil {
		return nil, err
	}
	return issues, nil
}

func (s *RedisStore) SaveIssue(ctx context.Context, namespace, repo, handler string, issue models.Issue) error {
	issueKey := fmt.Sprintf("issue:ns:%s:repo:%s:handler:%s:issue:%s", namespace, repo, handler, issue.ID)
	// We need to pass extra fields that are not in models.Issue but were in the HSet call in handlers_issue.go
	// In handlers_issue.go:
	// "agentDraft": draft (which is issue.Draft)
	// "pushBranch": strconv.FormatBool(pushBranch)

	return s.client.HSet(ctx, issueKey,
		"title", issue.Title,
		"sandbox", issue.Sandbox,
		"htmlurl", issue.HTMLURL,
		"sandboxReplica", issue.SandboxReplica,
		"branchURL", issue.BranchURL,
		"draft", issue.Draft,
		"agentDraft", issue.Draft, // Initial save sets agentDraft same as draft
		"pushBranch", fmt.Sprintf("%t", issue.PushBranch),
	).Err()
}

func (s *RedisStore) GetIssue(ctx context.Context, namespace, repo, handler, issueID string) (*models.Issue, error) {
	issueKey := fmt.Sprintf("issue:ns:%s:repo:%s:handler:%s:issue:%s", namespace, repo, handler, issueID)
	issueData, err := s.client.HGetAll(ctx, issueKey).Result()
	if err != nil {
		return nil, err
	}
	if len(issueData) == 0 {
		return nil, fmt.Errorf("issue not found")
	}

	pushBranch := false
	if val, ok := issueData["pushBranch"]; ok && val == "true" {
		pushBranch = true
	}

	issue := &models.Issue{
		ID:         issueID,
		Title:      issueData["title"],
		PushBranch: pushBranch,
	}

	if val, ok := issueData["htmlurl"]; ok {
		issue.HTMLURL = val
	}
	if val, ok := issueData["draft"]; ok {
		issue.Draft = val
	}
	if val, ok := issueData["sandbox"]; ok {
		issue.Sandbox = val
	}
	if val, ok := issueData["sandboxReplica"]; ok {
		issue.SandboxReplica = val
	}
	if val, ok := issueData["comment"]; ok {
		issue.Comment = val
	}
	if val, ok := issueData["branchURL"]; ok {
		issue.BranchURL = val
	}
	if val, ok := issueData["agentDraft"]; ok {
		issue.AgentDraft = val
	}

	return issue, nil
}

func (s *RedisStore) UpdateIssueDraft(ctx context.Context, namespace, repo, handler, issueID, draft string) error {
	issueKey := fmt.Sprintf("issue:ns:%s:repo:%s:handler:%s:issue:%s", namespace, repo, handler, issueID)
	return s.client.HSet(ctx, issueKey, "draft", draft).Err()
}

func (s *RedisStore) SaveIssueFeedback(ctx context.Context, owner, repo, handler, issueID, draft, agentDraft, prompt, configdir string) error {
	hfKey := fmt.Sprintf("hf:issue:githubuser:%s:repo:%s:handler:%s:pr:%s", owner, repo, handler, issueID)
	return s.client.HSet(ctx, hfKey,
		"draft", draft,
		"agentDraft", agentDraft,
		"prompt", prompt,
		"configdirname", configdir,
	).Err()
}

func (s *RedisStore) UpdateIssueComment(ctx context.Context, namespace, repo, handler, issueID, comment string) error {
	issueKey := fmt.Sprintf("issue:ns:%s:repo:%s:handler:%s:issue:%s", namespace, repo, handler, issueID)
	// This also clears the draft
	if err := s.client.HSet(ctx, issueKey, "comment", comment).Err(); err != nil {
		return err
	}
	return s.client.HSet(ctx, issueKey, "draft", "").Err()
}

func (s *RedisStore) DeleteIssue(ctx context.Context, namespace, repo, handler, issueID string) error {
	issueKey := fmt.Sprintf("issue:ns:%s:repo:%s:handler:%s:issue:%s", namespace, repo, handler, issueID)
	return s.client.Del(ctx, issueKey).Err()
}

// PopulateMockData populates Redis with mock data
func PopulateMockData(ctx context.Context, rdb *redis.Client) {
	// Create a temporary store to use the methods
	s := NewRedisStore(rdb)

	mockRepos := []struct {
		Name string
		URL  string
	}{
		{Name: "redis", URL: "https://github.com/redis/redis"},
		{Name: "linux", URL: "https://github.com/linux/linux"},
	}

	mockPRs := map[string][]models.PR{
		"redis": {
			{ID: "123", Title: "Feat: Add awesome feature", Draft: "This is a draft review."},
			{ID: "124", Title: "Fix: A really bad bug", Sandbox: "redis-pr-124", Review: "LGTM!"},
		},
		"linux": {
			{ID: "1", Title: "Docs: Update README", Sandbox: "linux-pr-1", Draft: "Few spelling mistakes. s/Nort/North/"},
			{ID: "2", Title: "Refactor: Improve performance"},
		},
	}

	for _, repo := range mockRepos {
		// Store repo URL (Mock data in default namespace)
		if err := s.SaveRepo(ctx, "default", repo.Name, repo.URL); err != nil {
			log.Printf("Failed to set repo URL in Redis: %v", err)
		}

		// Store PRs for the repo
		for _, pr := range mockPRs[repo.Name] {
			prKey := fmt.Sprintf("pr:ns:default:repo:%s:pr:%s", repo.Name, pr.ID)
			if err := rdb.HSet(ctx, prKey, "title", pr.Title, "draft", pr.Draft, "sandbox", pr.Sandbox, "review", pr.Review).Err(); err != nil {
				log.Printf("Failed to set PR info in Redis: %v", err)
			}
		}
	}
}
