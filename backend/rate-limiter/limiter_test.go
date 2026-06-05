package main

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// newTestRedis returns a redis.Client wired to an in-process miniredis. Both
// are torn down when the test ends.
func newTestRedis(t *testing.T) (*redis.Client, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb, mr
}

// ----- FIXED WINDOW -----

func TestCheckFixedWindow_AllowedThenDenied(t *testing.T) {
	rdb, _ := newTestRedis(t)
	ctx := context.Background()

	type want struct {
		allowed   bool
		remaining int
	}
	cases := []want{
		{true, 2},
		{true, 1},
		{true, 0},
	}

	for i, w := range cases {
		allowed, remaining, retryAfter, err := CheckFixedWindow(ctx, rdb, "fxd", 3, 60)
		if err != nil {
			t.Fatalf("req %d: %v", i+1, err)
		}
		if allowed != w.allowed || remaining != w.remaining || retryAfter != 0 {
			t.Errorf("req %d: got (%v, %d, %d), want (%v, %d, 0)",
				i+1, allowed, remaining, retryAfter, w.allowed, w.remaining)
		}
	}

	allowed, remaining, retryAfter, err := CheckFixedWindow(ctx, rdb, "fxd", 3, 60)
	if err != nil {
		t.Fatalf("4th: %v", err)
	}
	if allowed || remaining != 0 || retryAfter <= 0 {
		t.Errorf("4th (boundary): got (%v, %d, %d), want (false, 0, >0)",
			allowed, remaining, retryAfter)
	}
}

func TestCheckFixedWindow_ResetsInNewWindow(t *testing.T) {
	rdb, _ := newTestRedis(t)
	ctx := context.Background()

	// Fill the budget in a 1-second window.
	for i := 0; i < 3; i++ {
		allowed, _, _, err := CheckFixedWindow(ctx, rdb, "fxd-reset", 3, 1)
		if err != nil {
			t.Fatalf("setup %d: %v", i+1, err)
		}
		if !allowed {
			t.Fatalf("setup %d: expected allowed=true", i+1)
		}
	}
	if allowed, _, _, _ := CheckFixedWindow(ctx, rdb, "fxd-reset", 3, 1); allowed {
		t.Fatal("4th in same window should be denied")
	}

	// Wait for the next 1-second window. CheckFixedWindow uses Go's wall
	// clock (time.Now), so a real sleep is required — miniredis FastForward
	// won't affect it.
	time.Sleep(1100 * time.Millisecond)

	allowed, remaining, _, err := CheckFixedWindow(ctx, rdb, "fxd-reset", 3, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !allowed || remaining != 2 {
		t.Errorf("after window roll: got (%v, %d), want (true, 2)", allowed, remaining)
	}
}

// ----- SLIDING WINDOW -----

func TestCheckSlidingWindow_AllowedThenDenied(t *testing.T) {
	rdb, _ := newTestRedis(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		allowed, remaining, retryAfter, err := CheckSlidingWindow(ctx, rdb, "sld", 3, 60)
		if err != nil {
			t.Fatalf("req %d: %v", i+1, err)
		}
		wantRem := 2 - i
		if !allowed || remaining != wantRem || retryAfter != 0 {
			t.Errorf("req %d: got (%v, %d, %d), want (true, %d, 0)",
				i+1, allowed, remaining, retryAfter, wantRem)
		}
	}

	allowed, remaining, retryAfter, err := CheckSlidingWindow(ctx, rdb, "sld", 3, 60)
	if err != nil {
		t.Fatalf("4th: %v", err)
	}
	if allowed || remaining != 0 || retryAfter <= 0 {
		t.Errorf("4th (boundary): got (%v, %d, %d), want (false, 0, >0)",
			allowed, remaining, retryAfter)
	}
}

func TestCheckSlidingWindow_OlderEntriesAgeOut(t *testing.T) {
	rdb, mr := newTestRedis(t)
	ctx := context.Background()

	// Pin the miniredis clock so we can advance it deterministically. We use
	// SetTime (not FastForward) because miniredis's FastForward does not
	// advance the value returned by redis.call('TIME'), which our sliding
	// window script depends on.
	base := time.Now()
	mr.SetTime(base)

	for i := 0; i < 3; i++ {
		if a, _, _, err := CheckSlidingWindow(ctx, rdb, "sld-age", 3, 10); err != nil {
			t.Fatal(err)
		} else if !a {
			t.Fatalf("setup %d: expected allowed=true", i+1)
		}
	}
	if a, _, _, _ := CheckSlidingWindow(ctx, rdb, "sld-age", 3, 10); a {
		t.Fatal("4th should be denied")
	}

	// Push the clock past the window so ZREMRANGEBYSCORE drops every entry.
	mr.SetTime(base.Add(11 * time.Second))

	allowed, remaining, _, err := CheckSlidingWindow(ctx, rdb, "sld-age", 3, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !allowed || remaining != 2 {
		t.Errorf("after time advance: got (%v, %d), want (true, 2)", allowed, remaining)
	}
}

// ----- TOKEN BUCKET -----

func TestCheckTokenBucket_CapacityRespected(t *testing.T) {
	rdb, _ := newTestRedis(t)
	ctx := context.Background()

	// capacity=3, refill=1.0 tokens/sec. Three back-to-back calls should
	// drain the bucket faster than refill can keep up.
	for i := 0; i < 3; i++ {
		allowed, remaining, retryAfter, err := CheckTokenBucket(ctx, rdb, "tok", 3, 1.0)
		if err != nil {
			t.Fatalf("req %d: %v", i+1, err)
		}
		wantRem := 2 - i
		if !allowed || retryAfter != 0 {
			t.Errorf("req %d: got (%v, %d, %d), want (true, %d, 0)",
				i+1, allowed, remaining, retryAfter, wantRem)
		}
		if remaining != wantRem {
			t.Errorf("req %d: remaining=%d, want %d", i+1, remaining, wantRem)
		}
	}

	allowed, remaining, retryAfter, err := CheckTokenBucket(ctx, rdb, "tok", 3, 1.0)
	if err != nil {
		t.Fatalf("4th: %v", err)
	}
	if allowed || remaining != 0 {
		t.Errorf("4th: got (%v, %d), want (false, 0)", allowed, remaining)
	}
	if retryAfter < 1 {
		t.Errorf("4th: retryAfter=%d, want >=1 (≈ceil(1/refill))", retryAfter)
	}
}

func TestCheckTokenBucket_Refill(t *testing.T) {
	rdb, mr := newTestRedis(t)
	ctx := context.Background()

	// Pin the clock; use SetTime (not FastForward) so redis.call('TIME')
	// inside the Lua script sees the new value.
	base := time.Now()
	mr.SetTime(base)

	// Drain the bucket.
	for i := 0; i < 3; i++ {
		if a, _, _, err := CheckTokenBucket(ctx, rdb, "tok-refill", 3, 1.0); err != nil {
			t.Fatal(err)
		} else if !a {
			t.Fatalf("drain %d: expected allowed=true", i+1)
		}
	}
	if a, _, _, _ := CheckTokenBucket(ctx, rdb, "tok-refill", 3, 1.0); a {
		t.Fatal("empty bucket should deny")
	}

	// Advance the Redis clock 2 seconds → 2 tokens refilled.
	mr.SetTime(base.Add(2 * time.Second))

	for i := 0; i < 2; i++ {
		if a, _, _, err := CheckTokenBucket(ctx, rdb, "tok-refill", 3, 1.0); err != nil {
			t.Fatal(err)
		} else if !a {
			t.Errorf("post-refill %d: expected allowed=true", i+1)
		}
	}
	if a, _, _, _ := CheckTokenBucket(ctx, rdb, "tok-refill", 3, 1.0); a {
		t.Error("3rd after 2s refill should deny (only 2 tokens regenerated)")
	}
}

// ----- CONCURRENCY (the important one) -----
//
// 100 goroutines, limit/capacity 50. Atomicity of the script (or Redis INCR for
// fixed) must keep allowed == 50 exactly. Any over-admission is a real bug.

func TestConcurrentAccess_ExactAdmission(t *testing.T) {
	const goroutines = 100
	const limit = 50

	type checkFn func(ctx context.Context, rdb *redis.Client) (bool, error)
	cases := []struct {
		name string
		run  checkFn
	}{
		{"fixed", func(ctx context.Context, rdb *redis.Client) (bool, error) {
			a, _, _, err := CheckFixedWindow(ctx, rdb, "concurrent", limit, 60)
			return a, err
		}},
		{"sliding", func(ctx context.Context, rdb *redis.Client) (bool, error) {
			a, _, _, err := CheckSlidingWindow(ctx, rdb, "concurrent", limit, 60)
			return a, err
		}},
		{"token", func(ctx context.Context, rdb *redis.Client) (bool, error) {
			// refill rate ~zero so no refill happens during the test.
			a, _, _, err := CheckTokenBucket(ctx, rdb, "concurrent", limit, 0.001)
			return a, err
		}},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			rdb, _ := newTestRedis(t)
			ctx := context.Background()

			var allowed, denied int64
			var wg sync.WaitGroup
			wg.Add(goroutines)

			start := make(chan struct{})
			for i := 0; i < goroutines; i++ {
				go func() {
					defer wg.Done()
					<-start
					ok, err := tc.run(ctx, rdb)
					if err != nil {
						t.Errorf("call failed: %v", err)
						return
					}
					if ok {
						atomic.AddInt64(&allowed, 1)
					} else {
						atomic.AddInt64(&denied, 1)
					}
				}()
			}
			close(start)
			wg.Wait()

			if allowed != limit {
				t.Errorf("allowed=%d, want exactly %d — over- or under-admission means atomicity broke",
					allowed, limit)
			}
			if denied != goroutines-limit {
				t.Errorf("denied=%d, want %d", denied, goroutines-limit)
			}
		})
	}
}
