package main

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// CheckFixedWindow implements a fixed-window rate limiter backed by Redis.
// The window is aligned to UTC seconds (windowStart = now/windowSeconds * windowSeconds)
// so all replicas observe the same boundary.
func CheckFixedWindow(ctx context.Context, rdb *redis.Client, key string, limit int, windowSeconds int) (allowed bool, remaining int, retryAfter int, err error) {
	now := time.Now().Unix()
	window := int64(windowSeconds)
	windowStart := (now / window) * window
	redisKey := fmt.Sprintf("ratelimit:fixed:%s:%d", key, windowStart)

	count, err := rdb.Incr(ctx, redisKey).Result()
	if err != nil {
		return false, 0, 0, fmt.Errorf("incr %s: %w", redisKey, err)
	}

	if count == 1 {
		if err := rdb.Expire(ctx, redisKey, time.Duration(windowSeconds)*time.Second).Err(); err != nil {
			return false, 0, 0, fmt.Errorf("expire %s: %w", redisKey, err)
		}
	}

	if int(count) <= limit {
		return true, limit - int(count), 0, nil
	}

	retry := int((windowStart + window) - now)
	if retry < 0 {
		retry = 0
	}
	return false, 0, retry, nil
}
