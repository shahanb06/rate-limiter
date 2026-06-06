package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// brokenRedis returns a redis.Client pointed at a miniredis instance that has
// already been closed, so every command fails immediately.
func brokenRedis(t *testing.T) *redis.Client {
	t.Helper()
	mr := miniredis.RunT(t)
	addr := mr.Addr()
	mr.Close()
	rdb := redis.NewClient(&redis.Options{
		Addr:        addr,
		DialTimeout: 200 * time.Millisecond,
		ReadTimeout: 200 * time.Millisecond,
	})
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

// TestCheckUnaffectedWhenEventStreamFails proves that /check returns correct
// results and headers even when every event-stream XADD fails. The emitter
// targets a broken Redis; the limiter uses a working one.
func TestCheckUnaffectedWhenEventStreamFails(t *testing.T) {
	limiterRDB, _ := newTestRedis(t)
	streamRDB := brokenRedis(t)

	emitter := NewEventEmitter(streamRDB, "rl:events", 64, 100)
	ctx, cancel := context.WithCancel(context.Background())
	defer func() {
		cancel()
		select {
		case <-emitter.Done():
		case <-time.After(2 * time.Second):
			t.Error("emitter did not drain within 2s")
		}
	}()
	go emitter.Run(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc("/check", CheckHandler(limiterRDB, emitter))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	const n = 50
	const limit = 5
	url := srv.URL + "/check?key=stream-fail&algorithm=fixed&limit=" + strconv.Itoa(limit) + "&window=60"

	start := time.Now()
	var allowedCount, deniedCount int
	for i := 0; i < n; i++ {
		resp, err := http.Post(url, "", nil)
		if err != nil {
			t.Fatalf("req %d: %v", i+1, err)
		}
		var body CheckResponse
		_ = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()

		switch resp.StatusCode {
		case http.StatusOK:
			allowedCount++
			if !body.Allowed {
				t.Errorf("req %d: status=200 but allowed=false", i+1)
			}
		case http.StatusTooManyRequests:
			deniedCount++
			if body.Allowed {
				t.Errorf("req %d: status=429 but allowed=true", i+1)
			}
		default:
			t.Fatalf("req %d: unexpected status %d", i+1, resp.StatusCode)
		}
	}
	elapsed := time.Since(start)

	if allowedCount != limit {
		t.Errorf("allowed=%d, want %d", allowedCount, limit)
	}
	if deniedCount != n-limit {
		t.Errorf("denied=%d, want %d", deniedCount, n-limit)
	}

	// 50 requests should comfortably finish in well under a second even with a
	// broken emitter — if Emit were blocking on the failing pipeline, this
	// would balloon past the 200ms dial timeout per request.
	if elapsed > 3*time.Second {
		t.Errorf("50 requests took %v; emitter appears to be blocking /check", elapsed)
	}
}

// TestEmitDropCounterOnOverflow proves the channel-overflow drop path
// increments the drop counter.
func TestEmitDropCounterOnOverflow(t *testing.T) {
	rdb, _ := newTestRedis(t)
	// Buffer of 4; no drainer running, so the 5th emit onward overflows.
	emitter := NewEventEmitter(rdb, "rl:events", 4, 100)

	const total = 100
	for i := 0; i < total; i++ {
		emitter.Emit(Event{
			Key:       "k",
			Algorithm: AlgoFixed,
			Allowed:   true,
			Status:    200,
			TS:        time.Now(),
		})
	}

	want := uint64(total - 4)
	if got := emitter.Dropped(); got != want {
		t.Errorf("Dropped=%d, want %d (buffer 4, total %d)", got, want, total)
	}
}

// TestEmitDropCounterOnRedisFailure proves the writeBatch error path
// increments the drop counter by the batch size.
func TestEmitDropCounterOnRedisFailure(t *testing.T) {
	streamRDB := brokenRedis(t)
	emitter := NewEventEmitter(streamRDB, "rl:events", 256, 100)

	ctx, cancel := context.WithCancel(context.Background())
	go emitter.Run(ctx)
	defer func() {
		cancel()
		<-emitter.Done()
	}()

	const n = 50
	for i := 0; i < n; i++ {
		emitter.Emit(Event{
			Key:       "k",
			Algorithm: AlgoFixed,
			Allowed:   true,
			Status:    200,
			TS:        time.Now(),
		})
	}

	// Wait for the drainer's flush ticker (50ms) plus the broken-pipeline
	// dial timeout (200ms) to elapse. 2s is plenty.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if emitter.Dropped() >= n {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got := emitter.Dropped(); got < n {
		t.Errorf("Dropped=%d, want >= %d after Redis-write failures", got, n)
	}
}

// TestEmitNeverBlocks confirms Emit returns immediately even when the channel
// buffer is exhausted and the drainer is not running.
func TestEmitNeverBlocks(t *testing.T) {
	rdb, _ := newTestRedis(t)
	emitter := NewEventEmitter(rdb, "rl:events", 4, 100)

	done := make(chan struct{})
	go func() {
		for i := 0; i < 10_000; i++ {
			emitter.Emit(Event{
				Key:       "k",
				Algorithm: AlgoFixed,
				Allowed:   true,
				Status:    200,
				TS:        time.Now(),
			})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Emit blocked when buffer was full")
	}
}

// TestEmitDrainsToStream verifies the happy-path: events do reach the stream
// when Redis is healthy. Uses miniredis as the backing store.
func TestEmitDrainsToStream(t *testing.T) {
	rdb, _ := newTestRedis(t)
	emitter := NewEventEmitter(rdb, "rl:events", 64, 1000)

	ctx, cancel := context.WithCancel(context.Background())
	go emitter.Run(ctx)

	for i := 0; i < 5; i++ {
		emitter.Emit(Event{
			Key:       "alice",
			Algorithm: AlgoFixed,
			Allowed:   i < 3,
			Status:    200,
			TS:        time.Now(),
		})
	}

	bg := context.Background()
	deadline := time.Now().Add(1 * time.Second)
	var n int64
	for time.Now().Before(deadline) {
		got, err := rdb.XLen(bg, "rl:events").Result()
		if err == nil && got >= 5 {
			n = got
			break
		}
		n = got
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	<-emitter.Done()

	if n != 5 {
		t.Errorf("stream length=%d, want 5", n)
	}
}
