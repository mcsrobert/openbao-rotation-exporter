// Package collector runs the background collection loop and publishes
// immutable snapshots of its results.
package collector

import (
	"context"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mcsrobert/openbao-rotation-exporter/internal/openbao"
)

// Source reads rotation metadata.
type Source interface {
	Login(ctx context.Context) (string, error)
	CurrentVersionCreated(ctx context.Context, token, key string) (time.Time, error)
}

// Options configures a Collector.
type Options struct {
	Keys        map[string]time.Duration
	Interval    time.Duration
	Timeout     time.Duration
	Concurrency int
	Now         func() time.Time
	Logger      *slog.Logger
}

// KeyStatus is the latest known state of one key.
type KeyStatus struct {
	Key string
	// Up reports whether the most recent read of this key succeeded.
	Up bool
	// Due is the last known due time, kept while reads fail. It is zero until
	// the key has been read successfully since the process started.
	Due time.Time
	// Failures counts failed reads by reason.
	Failures map[string]uint64
}

// Snapshot is an immutable view of the collection state.
type Snapshot struct {
	Keys []KeyStatus
	// Attempted reports whether a collection has finished.
	Attempted bool
	// Success reports whether the latest collection authenticated and read at
	// least one key.
	Success bool
	// LastSuccess is zero until a collection has succeeded.
	LastSuccess time.Time
	Duration    time.Duration
	// Interval is the configured time between collections.
	Interval time.Duration
	// Failures counts failed collections by reason.
	Failures map[string]uint64
}

func (s *Snapshot) clone() *Snapshot {
	next := *s
	next.Keys = slices.Clone(s.Keys)
	for i := range next.Keys {
		next.Keys[i].Failures = maps.Clone(s.Keys[i].Failures)
	}
	next.Failures = maps.Clone(s.Failures)
	return &next
}

// Collector collects rotation metadata in the background.
type Collector struct {
	source      Source
	intervals   map[string]time.Duration
	interval    time.Duration
	timeout     time.Duration
	concurrency int
	now         func() time.Time
	logger      *slog.Logger
	snapshot    atomic.Pointer[Snapshot]
}

// New returns a Collector with an initial snapshot in which no key is up.
func New(source Source, options Options) *Collector {
	keys := slices.Sorted(maps.Keys(options.Keys))
	initial := &Snapshot{Keys: make([]KeyStatus, len(keys)), Interval: options.Interval, Failures: map[string]uint64{}}
	for i, key := range keys {
		initial.Keys[i] = KeyStatus{Key: key, Failures: map[string]uint64{}}
	}
	c := &Collector{
		source:      source,
		intervals:   options.Keys,
		interval:    options.Interval,
		timeout:     options.Timeout,
		concurrency: options.Concurrency,
		now:         options.Now,
		logger:      options.Logger,
	}
	c.snapshot.Store(initial)
	return c
}

// Snapshot returns the latest snapshot. Callers must not modify it.
func (c *Collector) Snapshot() *Snapshot {
	return c.snapshot.Load()
}

// Run collects immediately and then on every interval until ctx is canceled.
func (c *Collector) Run(ctx context.Context) {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		c.Collect(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Collect runs one collection and publishes the result. The new snapshot is
// published only after every read has settled, and not at all if ctx was
// canceled, so a shutdown is never recorded as a failure.
func (c *Collector) Collect(ctx context.Context) {
	start := c.now()
	collectCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	next := c.snapshot.Load().clone()
	next.Attempted = true

	token, err := c.source.Login(collectCtx)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		reason := openbao.ReasonOf(err)
		next.Success = false
		next.Failures[reason]++
		c.logger.Warn("login failed", "reason", reason, "error", err)
	} else {
		results := c.readKeys(collectCtx, token, next.Keys)
		if ctx.Err() != nil {
			return
		}
		next.Success = c.apply(next, results)
		if next.Success {
			next.LastSuccess = c.now()
		} else {
			next.Failures[allKeysFailed]++
		}
	}

	next.Duration = c.now().Sub(start)
	c.snapshot.Store(next)
	c.logger.Info("collection finished", "success", next.Success, "duration", next.Duration)
}

const allKeysFailed = "all_keys_failed"

type readResult struct {
	created time.Time
	err     error
}

func (c *Collector) readKeys(ctx context.Context, token string, keys []KeyStatus) []readResult {
	results := make([]readResult, len(keys))
	limit := make(chan struct{}, c.concurrency)
	var wg sync.WaitGroup
	for i, status := range keys {
		limit <- struct{}{}
		wg.Go(func() {
			defer func() { <-limit }()
			created, err := c.source.CurrentVersionCreated(ctx, token, status.Key)
			results[i] = readResult{created, err}
		})
	}
	wg.Wait()
	return results
}

// apply updates next from results and reports whether any read succeeded.
func (c *Collector) apply(next *Snapshot, results []readResult) bool {
	anySucceeded := false
	for i, result := range results {
		status := &next.Keys[i]
		if result.err != nil {
			reason := openbao.ReasonOf(result.err)
			status.Up = false
			status.Failures[reason]++
			c.logger.Warn("key read failed", "key", status.Key, "reason", reason, "error", result.err)
			continue
		}
		anySucceeded = true
		status.Up = true
		status.Due = result.created.Add(c.intervals[status.Key])
	}
	return anySucceeded
}
