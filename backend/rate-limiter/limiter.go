package main

import (
	"context"
	"fmt"
	"math/rand"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// Algorithm identifiers used by the API + logging.
const (
	AlgoFixed   = "fixed"
	AlgoSliding = "sliding"
	AlgoToken   = "token"
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

// CheckSlidingWindow implements a sliding-window rate limiter backed by a Redis
// sorted set. The script is fully atomic, and time is taken from Redis itself
// so all rate-limiter instances agree on the window boundary.
func CheckSlidingWindow(ctx context.Context, rdb *redis.Client, key string, limit int, windowSeconds int) (allowed bool, remaining int, retryAfter int, err error) {
	redisKey := fmt.Sprintf("ratelimit:sliding:%s", key)
	member := uniqueMember()

	res, err := slidingWindowScript.Run(ctx, rdb, []string{redisKey},
		limit, windowSeconds, member).Result()
	if err != nil {
		return false, 0, 0, fmt.Errorf("sliding window script: %w", err)
	}

	a, rem, retry, err := parseLuaTriple(res)
	if err != nil {
		return false, 0, 0, err
	}
	return a == 1, rem, retry, nil
}

// CheckTokenBucket implements a token-bucket rate limiter backed by a Redis hash.
// One token is requested per call. The script is atomic and uses Redis-side time.
func CheckTokenBucket(ctx context.Context, rdb *redis.Client, key string, capacity int, refillRatePerSec float64) (allowed bool, remaining int, retryAfter int, err error) {
	redisKey := fmt.Sprintf("ratelimit:token:%s", key)
	rate := strconv.FormatFloat(refillRatePerSec, 'f', -1, 64)

	res, err := tokenBucketScript.Run(ctx, rdb, []string{redisKey},
		capacity, rate, 1).Result()
	if err != nil {
		return false, 0, 0, fmt.Errorf("token bucket script: %w", err)
	}

	a, rem, retry, err := parseLuaTriple(res)
	if err != nil {
		return false, 0, 0, err
	}
	return a == 1, rem, retry, nil
}

// uniqueMember returns a sorted-set member that is effectively unique across
// concurrent callers. Members must be unique because ZADD with the same member
// just updates the score, which would silently drop a request.
func uniqueMember() string {
	return strconv.FormatInt(time.Now().UnixNano(), 10) + "-" +
		strconv.FormatUint(rand.Uint64(), 36)
}

// parseLuaTriple unwraps the {allowed, remaining, retry_after} integer table
// returned by both Lua scripts. go-redis decodes Lua arrays as []interface{}
// of int64s (or strings, depending on the value).
func parseLuaTriple(v interface{}) (allowed, remaining, retryAfter int, err error) {
	arr, ok := v.([]interface{})
	if !ok || len(arr) != 3 {
		return 0, 0, 0, fmt.Errorf("unexpected lua reply: %T %v", v, v)
	}
	allowed, err = toInt(arr[0])
	if err != nil {
		return 0, 0, 0, fmt.Errorf("allowed: %w", err)
	}
	remaining, err = toInt(arr[1])
	if err != nil {
		return 0, 0, 0, fmt.Errorf("remaining: %w", err)
	}
	retryAfter, err = toInt(arr[2])
	if err != nil {
		return 0, 0, 0, fmt.Errorf("retry_after: %w", err)
	}
	return allowed, remaining, retryAfter, nil
}

func toInt(v interface{}) (int, error) {
	switch x := v.(type) {
	case int64:
		return int(x), nil
	case int:
		return x, nil
	case string:
		n, err := strconv.Atoi(x)
		if err != nil {
			return 0, fmt.Errorf("parse %q: %w", x, err)
		}
		return n, nil
	default:
		return 0, fmt.Errorf("not an int: %T %v", v, v)
	}
}
