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

// PopulateMockData populates Redis with mock data
func PopulateMockData(ctx context.Context, rdb *redis.Client) {
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
		if err := rdb.HSet(ctx, fmt.Sprintf("repo:ns:default:name:%s", repo.Name), "url", repo.URL, "namespace", "default").Err(); err != nil {
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
