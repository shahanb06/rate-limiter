package main

import "github.com/redis/go-redis/v9"

// slidingWindowScript implements a sliding-window limiter on a Redis sorted set.
//
// KEYS[1] = sorted-set key
// ARGV[1] = limit                       (int)
// ARGV[2] = window seconds              (int)
// ARGV[3] = unique member               (string, must be unique per request)
//
// Returns {allowed, remaining, retry_after, reset_unix_seconds}.
// reset is the unix-seconds timestamp when the oldest currently-tracked entry
// will age out (i.e. when "remaining" goes up by one). 0 if no entries exist.
//
// Time is read from redis.call('TIME') so all replicas agree on "now" and the
// caller's clock does not influence the window boundary.
var slidingWindowScript = redis.NewScript(`
local key       = KEYS[1]
local limit     = tonumber(ARGV[1])
local window    = tonumber(ARGV[2])
local member    = ARGV[3]

local t         = redis.call('TIME')
local now_us    = tonumber(t[1]) * 1000000 + tonumber(t[2])
local window_us = window * 1000000
local cutoff    = now_us - window_us

redis.call('ZREMRANGEBYSCORE', key, '-inf', cutoff)
local count = redis.call('ZCARD', key)

local reset = 0

if count < limit then
    redis.call('ZADD', key, now_us, member)
    redis.call('EXPIRE', key, window)
    local oldest = redis.call('ZRANGE', key, 0, 0, 'WITHSCORES')
    if #oldest >= 2 then
        local oldest_score = tonumber(oldest[2])
        reset = math.ceil((oldest_score + window_us) / 1000000)
    end
    return {1, limit - count - 1, 0, reset}
end

local oldest = redis.call('ZRANGE', key, 0, 0, 'WITHSCORES')
local retry  = 0
if #oldest >= 2 then
    local oldest_score = tonumber(oldest[2])
    local age_out_us   = oldest_score + window_us
    retry = math.ceil((age_out_us - now_us) / 1000000)
    if retry < 0 then retry = 0 end
    reset = math.ceil(age_out_us / 1000000)
end
return {0, 0, retry, reset}
`)

// tokenBucketScript implements a token-bucket limiter on a Redis hash.
//
// KEYS[1] = hash key
// ARGV[1] = capacity                    (int, max tokens)
// ARGV[2] = refill rate (tokens/sec)    (float)
// ARGV[3] = requested tokens            (int, usually 1)
//
// Returns {allowed, remaining (floor), retry_after_seconds, reset_unix_seconds}.
// reset is the unix-seconds timestamp when floor(tokens) will increase by one
// (i.e. when "remaining" goes up). 0 if the bucket is already at capacity.
//
// Time is read from redis.call('TIME') so the bucket refills using Redis's
// clock, avoiding skew across multiple rate-limiter instances.
var tokenBucketScript = redis.NewScript(`
local key       = KEYS[1]
local capacity  = tonumber(ARGV[1])
local rate      = tonumber(ARGV[2])
local requested = tonumber(ARGV[3])

local t         = redis.call('TIME')
local now       = tonumber(t[1]) + tonumber(t[2]) / 1000000

local data        = redis.call('HMGET', key, 'tokens', 'last_refill')
local tokens      = tonumber(data[1])
local last_refill = tonumber(data[2])

if tokens == nil then
    tokens      = capacity
    last_refill = now
end

local elapsed = now - last_refill
if elapsed < 0 then elapsed = 0 end
tokens = math.min(capacity, tokens + elapsed * rate)

local allowed     = 0
local retry_after = 0
if tokens >= requested then
    tokens  = tokens - requested
    allowed = 1
else
    local deficit = requested - tokens
    retry_after   = math.ceil(deficit / rate)
end

redis.call('HSET', key, 'tokens', tokens, 'last_refill', now)
local ttl = math.ceil(capacity / rate) + 60
redis.call('EXPIRE', key, ttl)

local reset = 0
if tokens < capacity then
    local next_int = math.floor(tokens) + 1
    if next_int > capacity then next_int = capacity end
    if tokens < next_int then
        reset = math.ceil(now + (next_int - tokens) / rate)
    end
end

return {allowed, math.floor(tokens), retry_after, reset}
`)
