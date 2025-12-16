package store

import (
	"context"
	"fmt"
	"log"

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
