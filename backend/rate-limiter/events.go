package main

import (
	"context"
	"log/slog"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// Event is a single /check observation emitted to the analytics stream.
// TS is captured at the moment of the check, not when the worker writes it.
type Event struct {
	Key       string
	Algorithm string
	Allowed   bool
	Status    int
	TS        time.Time
}

// EventEmitter buffers Events in a channel and drains them to a Redis Stream
// from a single background goroutine. Emit is non-blocking and fire-and-forget:
// if the buffer is full or Redis is unreachable, events are dropped.
type EventEmitter struct {
	rdb     *redis.Client
	ch      chan Event
	stream  string
	maxLen  int64
	batchN  int
	flushIv time.Duration

	closed atomic.Bool
	done   chan struct{}
}

// NewEventEmitter constructs an emitter writing to streamName with an
// approximate MAXLEN trim of maxLen. bufferSize controls the channel depth;
// emits that would block are dropped.
func NewEventEmitter(rdb *redis.Client, streamName string, bufferSize int, maxLen int64) *EventEmitter {
	return &EventEmitter{
		rdb:     rdb,
		ch:      make(chan Event, bufferSize),
		stream:  streamName,
		maxLen:  maxLen,
		batchN:  100,
		flushIv: 50 * time.Millisecond,
		done:    make(chan struct{}),
	}
}

// Emit attempts a non-blocking send. Drops the event if the buffer is full or
// the emitter is closed. Safe to call from any goroutine; never blocks.
func (e *EventEmitter) Emit(ev Event) {
	if e.closed.Load() {
		return
	}
	select {
	case e.ch <- ev:
	default:
		// Buffer full — drop. Day 7 will add a counter.
	}
}

// Run drains the channel until ctx is cancelled, then flushes whatever is
// buffered and returns. Intended to be called once in its own goroutine.
func (e *EventEmitter) Run(ctx context.Context) {
	defer close(e.done)

	batch := make([]Event, 0, e.batchN)
	ticker := time.NewTicker(e.flushIv)
	defer ticker.Stop()

	flush := func() {
		if len(batch) == 0 {
			return
		}
		e.writeBatch(batch)
		batch = batch[:0]
	}

	for {
		select {
		case <-ctx.Done():
			e.closed.Store(true)
			// Drain whatever is already queued. New Emit calls now no-op.
			for {
				select {
				case ev := <-e.ch:
					batch = append(batch, ev)
					if len(batch) >= e.batchN {
						flush()
					}
				default:
					flush()
					return
				}
			}
		case ev := <-e.ch:
			batch = append(batch, ev)
			if len(batch) >= e.batchN {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

// Done returns a channel closed after Run has finished its final drain.
func (e *EventEmitter) Done() <-chan struct{} { return e.done }

// writeBatch pipelines XADDs for the batch. On error, the batch is logged and
// dropped — analytics is best-effort and must never feed back into /check.
func (e *EventEmitter) writeBatch(batch []Event) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	pipe := e.rdb.Pipeline()
	for _, ev := range batch {
		allowed := "0"
		if ev.Allowed {
			allowed = "1"
		}
		pipe.XAdd(ctx, &redis.XAddArgs{
			Stream: e.stream,
			MaxLen: e.maxLen,
			Approx: true,
			Values: map[string]interface{}{
				"key":       ev.Key,
				"algorithm": ev.Algorithm,
				"allowed":   allowed,
				"status":    strconv.Itoa(ev.Status),
				"ts":        strconv.FormatInt(ev.TS.UnixMilli(), 10),
			},
		})
	}
	if _, err := pipe.Exec(ctx); err != nil {
		slog.Warn("event stream write failed",
			"err", err.Error(),
			"dropped", len(batch),
		)
	}
}
