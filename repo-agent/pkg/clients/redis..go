package clients

import (
	"context"
	"os"

	redis "github.com/go-redis/redis/v8"
	"k8s.io/klog/v2"
)

// NewRedisClient creates a new Redis client
func NewRedisClient() *redis.Client {
	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "localhost:6379"
	}

	return redis.NewClient(&redis.Options{
		Addr: redisAddr,
	})
}

func EnsureRedisClient(rdb *redis.Client) *redis.Client {
	// Ping redis to ensure connection
	_, err := rdb.Ping(context.Background()).Result()
	if err != nil {
		klog.Fatalf("Failed to connect to Redis: %v", err)
	}
	return rdb
}
