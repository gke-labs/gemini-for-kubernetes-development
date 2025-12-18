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

func (s *RedisStore) RepoKey(namespace, name string) string {
	return fmt.Sprintf("repo:ns:%s:name:%s", namespace, name)
}

func (s *RedisStore) SaveRepo(ctx context.Context, namespace, name, url string) error {
	key := s.RepoKey(namespace, name)
	return s.client.HSet(ctx, key, "url", url, "namespace", namespace).Err()
}

func (s *RedisStore) DeleteRepo(ctx context.Context, namespace, name string) error {
	key := s.RepoKey(namespace, name)
	return s.client.Del(ctx, key).Err()
}

func (s *RedisStore) ListRepos(ctx context.Context, namespace string) ([]string, error) {
	prefix := s.RepoKey(namespace, "*")
	var repoNames []string
	iter := s.client.Scan(ctx, 0, prefix, 0).Iterator()
	for iter.Next(ctx) {
		key := iter.Val()
		repoName := key[len(prefix)-1:]
		repoNames = append(repoNames, repoName)
	}
	if err := iter.Err(); err != nil {
		return nil, err
	}
	return repoNames, nil
}

func (s *RedisStore) IssueKey(namespace, repo, handler, issueID string) string {
	return fmt.Sprintf("issue:ns:%s:repo:%s:handler:%s:issue:%s", namespace, repo, handler, issueID)
}

func (s *RedisStore) ListIssues(ctx context.Context, namespace, repo, handler string) ([]models.Issue, error) {
	issues := []models.Issue{}
	issueKeyPrefix := s.IssueKey(namespace, repo, handler, "*")
	iter := s.client.Scan(ctx, 0, issueKeyPrefix, 0).Iterator()
	for iter.Next(ctx) {
		key := iter.Val()
		parts := strings.Split(key, ":")
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

		issues = append(issues, issue)
	}
	if err := iter.Err(); err != nil {
		return nil, err
	}
	return issues, nil
}

func (s *RedisStore) SaveIssue(ctx context.Context, namespace, repo, handler string, issue models.Issue) error {
	issueKey := s.IssueKey(namespace, repo, handler, issue.ID)

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
	issueKey := s.IssueKey(namespace, repo, handler, issueID)
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
	issueKey := s.IssueKey(namespace, repo, handler, issueID)
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
	issueKey := s.IssueKey(namespace, repo, handler, issueID)
	// This also clears the draft
	if err := s.client.HSet(ctx, issueKey, "comment", comment).Err(); err != nil {
		return err
	}
	return s.client.HSet(ctx, issueKey, "draft", "").Err()
}

func (s *RedisStore) DeleteIssue(ctx context.Context, namespace, repo, handler, issueID string) error {
	issueKey := s.IssueKey(namespace, repo, handler, issueID)
	return s.client.Del(ctx, issueKey).Err()
}

func (s *RedisStore) DevSandboxKey(namespace, repo, name string) string {
	return fmt.Sprintf("dev:ns:%s:repo:%s:dev:%s", namespace, repo, name)
}

func (s *RedisStore) ListDevSandboxes(ctx context.Context, namespace, repo string) ([]models.DevSandbox, error) {
	sandboxes := []models.DevSandbox{}
	// prefix: dev:ns:NAMESPACE:repo:REPO:dev:*
	prefix := s.DevSandboxKey(namespace, repo, "*")
	iter := s.client.Scan(ctx, 0, prefix, 0).Iterator()
	for iter.Next(ctx) {
		key := iter.Val()
		parts := strings.Split(key, ":")
		// dev:ns:NAMESPACE:repo:REPO:dev:NAME
		// 0   1  2         3    4    5   6
		if len(parts) != 7 {
			continue
		}
		name := parts[6]

		data, err := s.client.HGetAll(ctx, key).Result()
		if err != nil {
			log.Printf("Failed to get DevSandbox %s from Redis for repo %s: %v", name, repo, err)
			continue
		}

		sandbox := models.DevSandbox{
			Name: name,
		}
		if val, ok := data["sandbox"]; ok {
			sandbox.Sandbox = val
		}
		if val, ok := data["sandboxReplica"]; ok {
			sandbox.SandboxReplica = val
		}
		if val, ok := data["branchURL"]; ok {
			sandbox.BranchURL = val
		}
		if val, ok := data["branch"]; ok {
			sandbox.Branch = val
		}

		sandboxes = append(sandboxes, sandbox)
	}
	if err := iter.Err(); err != nil {
		return nil, err
	}
	return sandboxes, nil
}

func (s *RedisStore) SaveDevSandbox(ctx context.Context, namespace, repo string, sandbox models.DevSandbox) error {
	key := s.DevSandboxKey(namespace, repo, sandbox.Name)
	return s.client.HSet(ctx, key,
		"sandbox", sandbox.Sandbox,
		"branch", sandbox.Branch,
		"branchURL", sandbox.BranchURL,
		"sandboxReplica", sandbox.SandboxReplica,
	).Err()
}

func (s *RedisStore) DeleteDevSandbox(ctx context.Context, namespace, repo, name string) error {
	key := s.DevSandboxKey(namespace, repo, name)
	return s.client.Del(ctx, key).Err()
}

func (s *RedisStore) PRKey(namespace, repo, prID string) string {
	return fmt.Sprintf("pr:ns:%s:repo:%s:pr:%s", namespace, repo, prID)
}

func (s *RedisStore) ListPRs(ctx context.Context, namespace, repo string) ([]models.PR, error) {
	prs := []models.PR{}
	repoPRKeyPrefix := s.PRKey(namespace, repo, "*")
	iter := s.client.Scan(ctx, 0, repoPRKeyPrefix, 0).Iterator()
	for iter.Next(ctx) {
		key := iter.Val()
		prID := key[len(repoPRKeyPrefix)-1:]
		prData, err := s.client.HGetAll(ctx, key).Result()
		if err != nil {
			log.Printf("Failed to get PR %s from Redis for repo %s: %v", prID, repo, err)
			continue
		}
		pr := models.PR{
			ID:    prID,
			Title: prData["title"],
		}

		if val, ok := prData["htmlurl"]; ok {
			pr.HTMLURL = val
		}
		if val, ok := prData["diffurl"]; ok {
			pr.DiffURL = val
		}
		if val, ok := prData["draft"]; ok {
			pr.Draft = val
		}
		if val, ok := prData["sandbox"]; ok {
			pr.Sandbox = val
		}
		if val, ok := prData["sandboxReplica"]; ok {
			pr.SandboxReplica = val
		}
		if val, ok := prData["review"]; ok {
			pr.Review = val
		}
		if val, ok := prData["agentDraft"]; ok {
			pr.AgentDraft = val
		}
		prs = append(prs, pr)
	}
	if err := iter.Err(); err != nil {
		return nil, err
	}
	return prs, nil
}

func (s *RedisStore) SavePR(ctx context.Context, namespace, repo string, pr models.PR) error {
	prKey := s.PRKey(namespace, repo, pr.ID)
	// Ensure the URL is in Redis
	return s.client.HSet(ctx, prKey,
		"title", pr.Title,
		"sandbox", pr.Sandbox,
		"htmlurl", pr.HTMLURL,
		"diffurl", pr.DiffURL,
		"sandboxReplica", pr.SandboxReplica,
		"draft", pr.Draft,
		"agentDraft", pr.AgentDraft,
	).Err()
}

func (s *RedisStore) GetPR(ctx context.Context, namespace, repo, prID string) (*models.PR, error) {
	prKey := s.PRKey(namespace, repo, prID)
	prData, err := s.client.HGetAll(ctx, prKey).Result()
	if err != nil {
		return nil, err
	}
	if len(prData) == 0 {
		return nil, fmt.Errorf("PR not found")
	}

	pr := &models.PR{
		ID:    prID,
		Title: prData["title"],
	}

	if val, ok := prData["htmlurl"]; ok {
		pr.HTMLURL = val
	}
	if val, ok := prData["diffurl"]; ok {
		pr.DiffURL = val
	}
	if val, ok := prData["draft"]; ok {
		pr.Draft = val
	}
	if val, ok := prData["sandbox"]; ok {
		pr.Sandbox = val
	}
	if val, ok := prData["sandboxReplica"]; ok {
		pr.SandboxReplica = val
	}
	if val, ok := prData["review"]; ok {
		pr.Review = val
	}
	if val, ok := prData["agentDraft"]; ok {
		pr.AgentDraft = val
	}

	return pr, nil
}

func (s *RedisStore) UpdatePRDraft(ctx context.Context, namespace, repo, prID, draft string) error {
	prKey := s.PRKey(namespace, repo, prID)
	return s.client.HSet(ctx, prKey, "draft", draft).Err()
}

func (s *RedisStore) UpdatePRReview(ctx context.Context, namespace, repo, prID, review string) error {
	prKey := s.PRKey(namespace, repo, prID)
	if err := s.client.HSet(ctx, prKey, "review", review).Err(); err != nil {
		return err
	}
	return s.client.HSet(ctx, prKey, "draft", "").Err()
}

func (s *RedisStore) SavePRFeedback(ctx context.Context, owner, repo, prID, draft, agentDraft, prompt, configdir string) error {
	hfKey := fmt.Sprintf("hf:review:githubuser:%s:repo:%s:pr:%s", owner, repo, prID)
	return s.client.HSet(ctx, hfKey,
		"draft", draft,
		"agentDraft", agentDraft,
		"prompt", prompt,
		"configdir", configdir,
	).Err()
}

func (s *RedisStore) DeletePR(ctx context.Context, namespace, repo, prID string) error {
	prKey := s.PRKey(namespace, repo, prID)
	// Clean up Redis keys
	if err := s.client.HDel(ctx, prKey, "review", "draft", "sandbox", "htmlurl", "title").Err(); err != nil {
		return err
	}
	return s.client.Del(ctx, prKey).Err()
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
