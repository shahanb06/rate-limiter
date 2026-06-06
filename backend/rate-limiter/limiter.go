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
//
// reset is the unix-seconds timestamp when the current window ends.
func CheckFixedWindow(ctx context.Context, rdb *redis.Client, key string, limit int, windowSeconds int) (allowed bool, remaining int, retryAfter int, reset int64, err error) {
	now := time.Now().Unix()
	window := int64(windowSeconds)
	windowStart := (now / window) * window
	reset = windowStart + window
	redisKey := fmt.Sprintf("ratelimit:fixed:%s:%d", key, windowStart)

	count, err := rdb.Incr(ctx, redisKey).Result()
	if err != nil {
		return false, 0, 0, 0, fmt.Errorf("incr %s: %w", redisKey, err)
	}

	if count == 1 {
		if err := rdb.Expire(ctx, redisKey, time.Duration(windowSeconds)*time.Second).Err(); err != nil {
			return false, 0, 0, 0, fmt.Errorf("expire %s: %w", redisKey, err)
		}
	}

	if int(count) <= limit {
		return true, limit - int(count), 0, reset, nil
	}

	retry := int(reset - now)
	if retry < 0 {
		retry = 0
	}
	return false, 0, retry, reset, nil
}

// CheckSlidingWindow implements a sliding-window rate limiter backed by a Redis
// sorted set. The script is fully atomic, and time is taken from Redis itself
// so all rate-limiter instances agree on the window boundary.
//
// reset is the unix-seconds timestamp when the oldest currently-tracked entry
// ages out (i.e. when "remaining" goes up). 0 means there are no entries.
func CheckSlidingWindow(ctx context.Context, rdb *redis.Client, key string, limit int, windowSeconds int) (allowed bool, remaining int, retryAfter int, reset int64, err error) {
	redisKey := fmt.Sprintf("ratelimit:sliding:%s", key)
	member := uniqueMember()

	res, err := slidingWindowScript.Run(ctx, rdb, []string{redisKey},
		limit, windowSeconds, member).Result()
	if err != nil {
		return false, 0, 0, 0, fmt.Errorf("sliding window script: %w", err)
	}

	vals, err := parseLuaInts(res, 4)
	if err != nil {
		return false, 0, 0, 0, err
	}
	return vals[0] == 1, int(vals[1]), int(vals[2]), vals[3], nil
}

// CheckTokenBucket implements a token-bucket rate limiter backed by a Redis hash.
// One token is requested per call. The script is atomic and uses Redis-side time.
//
// reset is the unix-seconds timestamp when floor(tokens) will increase by one
// (i.e. when "remaining" goes up). 0 means the bucket is already at capacity.
func CheckTokenBucket(ctx context.Context, rdb *redis.Client, key string, capacity int, refillRatePerSec float64) (allowed bool, remaining int, retryAfter int, reset int64, err error) {
	redisKey := fmt.Sprintf("ratelimit:token:%s", key)
	rate := strconv.FormatFloat(refillRatePerSec, 'f', -1, 64)

	res, err := tokenBucketScript.Run(ctx, rdb, []string{redisKey},
		capacity, rate, 1).Result()
	if err != nil {
		return false, 0, 0, 0, fmt.Errorf("token bucket script: %w", err)
	}

	vals, err := parseLuaInts(res, 4)
	if err != nil {
		return false, 0, 0, 0, err
	}
	return vals[0] == 1, int(vals[1]), int(vals[2]), vals[3], nil
}

// uniqueMember returns a sorted-set member that is effectively unique across
// concurrent callers. Members must be unique because ZADD with the same member
// just updates the score, which would silently drop a request.
func uniqueMember() string {
	return strconv.FormatInt(time.Now().UnixNano(), 10) + "-" +
		strconv.FormatUint(rand.Uint64(), 36)
}

// parseLuaInts unwraps a Lua array reply of n integers. go-redis decodes Lua
// arrays as []interface{} of int64s (or strings, depending on the value).
func parseLuaInts(v interface{}, n int) ([]int64, error) {
	arr, ok := v.([]interface{})
	if !ok || len(arr) != n {
		return nil, fmt.Errorf("unexpected lua reply: %T %v (expected %d elements)", v, v, n)
	}
	out := make([]int64, n)
	for i, x := range arr {
		iv, err := toInt64(x)
		if err != nil {
			return nil, fmt.Errorf("element %d: %w", i, err)
		}
		out[i] = iv
	}
	return out, nil
}

func toInt64(v interface{}) (int64, error) {
	switch x := v.(type) {
	case int64:
		return x, nil
	case int:
		return int64(x), nil
	case string:
		n, err := strconv.ParseInt(x, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parse %q: %w", x, err)
		}
		return n, nil
	default:
		return 0, fmt.Errorf("not an int: %T %v", v, v)
	}
}
