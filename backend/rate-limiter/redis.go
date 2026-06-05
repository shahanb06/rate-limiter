package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/redis/go-redis/v9"
)

// NewRedisClient parses REDIS_URL (defaulting to redis://localhost:6379) and
// returns a client that has been verified reachable. It retries the initial
// ping up to 3 times with a 1s pause between attempts.
func NewRedisClient() (*redis.Client, error) {
	url := os.Getenv("REDIS_URL")
	if url == "" {
		url = "redis://localhost:6379"
	}

	opt, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parse REDIS_URL %q: %w", url, err)
	}

	client := redis.NewClient(opt)

	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := client.Ping(ctx).Err()
		cancel()
		if err == nil {
			return client, nil
		}
		lastErr = err
		if attempt < 3 {
			time.Sleep(1 * time.Second)
		}
	}

	_ = client.Close()
	return nil, fmt.Errorf("redis unreachable after 3 attempts: %w", lastErr)
}

// Ping verifies the Redis connection is alive.
func Ping(ctx context.Context, rdb *redis.Client) error {
	return rdb.Ping(ctx).Err()
}
