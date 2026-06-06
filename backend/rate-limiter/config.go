package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/redis/go-redis/v9"
)

// LimitConfig is a per-key rate-limit configuration stored as a Redis hash.
// Only the fields relevant to the algorithm are populated/serialized.
type LimitConfig struct {
	Algorithm string  `json:"algorithm"`
	Limit     int     `json:"limit,omitempty"`
	Window    int     `json:"window,omitempty"`
	Capacity  int     `json:"capacity,omitempty"`
	Refill    float64 `json:"refill,omitempty"`
}

// ErrConfigNotFound signals that no config hash exists for the given key.
var ErrConfigNotFound = errors.New("config not found")

func configRedisKey(key string) string {
	return "ratelimit:config:" + key
}

// GetConfig reads the config hash for key. Returns ErrConfigNotFound if no
// hash exists.
func GetConfig(ctx context.Context, rdb *redis.Client, key string) (*LimitConfig, error) {
	data, err := rdb.HGetAll(ctx, configRedisKey(key)).Result()
	if err != nil {
		return nil, fmt.Errorf("hgetall config: %w", err)
	}
	if len(data) == 0 {
		return nil, ErrConfigNotFound
	}

	cfg := &LimitConfig{Algorithm: data["algorithm"]}
	if cfg.Algorithm == "" {
		return nil, fmt.Errorf("stored config missing 'algorithm' field")
	}
	if v, ok := data["limit"]; ok && v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("stored 'limit' invalid: %w", err)
		}
		cfg.Limit = n
	}
	if v, ok := data["window"]; ok && v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("stored 'window' invalid: %w", err)
		}
		cfg.Window = n
	}
	if v, ok := data["capacity"]; ok && v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("stored 'capacity' invalid: %w", err)
		}
		cfg.Capacity = n
	}
	if v, ok := data["refill"]; ok && v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return nil, fmt.Errorf("stored 'refill' invalid: %w", err)
		}
		cfg.Refill = f
	}
	return cfg, nil
}

// SetConfig replaces the config hash for key. Existing fields are cleared first
// so switching algorithms doesn't leave stale fields behind.
func SetConfig(ctx context.Context, rdb *redis.Client, key string, cfg *LimitConfig) error {
	fields := map[string]interface{}{
		"algorithm": cfg.Algorithm,
	}
	switch cfg.Algorithm {
	case AlgoFixed, AlgoSliding:
		fields["limit"] = strconv.Itoa(cfg.Limit)
		fields["window"] = strconv.Itoa(cfg.Window)
	case AlgoToken:
		fields["capacity"] = strconv.Itoa(cfg.Capacity)
		fields["refill"] = strconv.FormatFloat(cfg.Refill, 'f', -1, 64)
	}

	redisKey := configRedisKey(key)
	pipe := rdb.TxPipeline()
	pipe.Del(ctx, redisKey)
	pipe.HSet(ctx, redisKey, fields)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("store config: %w", err)
	}
	return nil
}

// ValidateConfig returns nil if cfg is a usable rate-limit configuration.
func ValidateConfig(cfg *LimitConfig) error {
	switch cfg.Algorithm {
	case AlgoFixed, AlgoSliding:
		if cfg.Limit <= 0 {
			return fmt.Errorf("limit must be a positive integer")
		}
		if cfg.Window <= 0 {
			return fmt.Errorf("window must be a positive integer")
		}
	case AlgoToken:
		if cfg.Capacity <= 0 {
			return fmt.Errorf("capacity must be a positive integer")
		}
		if cfg.Refill <= 0 {
			return fmt.Errorf("refill must be a positive number")
		}
	case "":
		return fmt.Errorf("algorithm is required")
	default:
		return fmt.Errorf("algorithm must be one of: fixed, sliding, token")
	}
	return nil
}
