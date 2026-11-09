package collector

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"maps"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/mcsrobert/openbao-rotation-exporter/internal/openbao"
)

var (
	base   = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	forbid = &openbao.Error{Reason: openbao.ReasonForbidden, Err: errors.New("denied")}
	down   = &openbao.Error{Reason: openbao.ReasonUnavailable, Err: errors.New("down")}
)

type fakeSource struct {
	mu       sync.Mutex
	loginErr error
	created  map[string]time.Time
	errs     map[string]error
	onRead   func(ctx context.Context, key string)
}

func (f *fakeSource) Login(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return "token", f.loginErr
}

func (f *fakeSource) CurrentVersionCreated(ctx context.Context, token, key string) (time.Time, error) {
	if token != "token" {
		return time.Time{}, errors.New("wrong token")
	}
	if f.onRead != nil {
		f.onRead(ctx, key)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.errs[key]; err != nil {
		return time.Time{}, err
	}
	return f.created[key], nil
}

func (f *fakeSource) set(update func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	update()
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newCollector(source Source, clock *fakeClock, keys map[string]time.Duration) *Collector {
	return New(source, Options{
		Keys:        keys,
		Interval:    5 * time.Minute,
		Timeout:     time.Minute,
		Concurrency: 2,
		Now:         clock.Now,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

func keyStatus(t *testing.T, snapshot *Snapshot, key string) KeyStatus {
	t.Helper()
	for _, status := range snapshot.Keys {
		if status.Key == key {
			return status
		}
	}
	t.Fatalf("key %q not in snapshot", key)
	return KeyStatus{}
}

func TestInitialSnapshot(t *testing.T) {
	c := newCollector(&fakeSource{}, &fakeClock{now: base}, map[string]time.Duration{"b": time.Hour, "a": time.Hour})
	s := c.Snapshot()
	if s.Attempted || s.Success || !s.LastSuccess.IsZero() {
		t.Fatalf("unexpected initial state: %+v", s)
	}
	if len(s.Keys) != 2 || s.Keys[0].Key != "a" || s.Keys[1].Key != "b" {
		t.Fatalf("keys are not sorted: %+v", s.Keys)
	}
	if s.Keys[0].Up || !s.Keys[0].Due.IsZero() {
		t.Fatalf("key should start down with no due time: %+v", s.Keys[0])
	}
	if s.Interval != 5*time.Minute {
		t.Fatalf("interval = %v, want 5m", s.Interval)
	}
}

func TestSuccessfulCollection(t *testing.T) {
	clock := &fakeClock{now: base}
	source := &fakeSource{created: map[string]time.Time{"a": base.Add(-time.Hour), "b": base.Add(-2 * time.Hour)}}
	c := newCollector(source, clock, map[string]time.Duration{"a": 24 * time.Hour, "b": 48 * time.Hour})

	c.Collect(t.Context())

	s := c.Snapshot()
	if !s.Attempted || !s.Success || !s.LastSuccess.Equal(base) || s.Interval != 5*time.Minute {
		t.Fatalf("unexpected state: %+v", s)
	}
	if got := keyStatus(t, s, "a"); !got.Up || !got.Due.Equal(base.Add(23*time.Hour)) {
		t.Fatalf("a = %+v", got)
	}
	if got := keyStatus(t, s, "b"); !got.Up || !got.Due.Equal(base.Add(46*time.Hour)) {
		t.Fatalf("b = %+v", got)
	}
}

func TestFailedKeyKeepsLastKnownDueTime(t *testing.T) {
	clock := &fakeClock{now: base}
	source := &fakeSource{created: map[string]time.Time{"a": base, "b": base}}
	c := newCollector(source, clock, map[string]time.Duration{"a": time.Hour, "b": time.Hour})
	c.Collect(t.Context())

	clock.Advance(5 * time.Minute)
	source.set(func() { source.errs = map[string]error{"a": forbid} })
	c.Collect(t.Context())

	s := c.Snapshot()
	if !s.Success || !s.LastSuccess.Equal(clock.Now()) {
		t.Fatalf("a partial failure should still count as success: %+v", s)
	}
	a := keyStatus(t, s, "a")
	if a.Up {
		t.Fatal("a should be down")
	}
	if !a.Due.Equal(base.Add(time.Hour)) {
		t.Fatalf("a lost its due time: %s", a.Due)
	}
	if a.Failures[openbao.ReasonForbidden] != 1 {
		t.Fatalf("failures = %v", a.Failures)
	}
	if !keyStatus(t, s, "b").Up {
		t.Fatal("b should stay up")
	}

	source.set(func() {
		source.errs = nil
		source.created["a"] = base.Add(10 * time.Minute)
	})
	c.Collect(t.Context())
	a = keyStatus(t, c.Snapshot(), "a")
	if !a.Up || !a.Due.Equal(base.Add(70*time.Minute)) {
		t.Fatalf("a did not recover: %+v", a)
	}
	if a.Failures[openbao.ReasonForbidden] != 1 {
		t.Fatalf("failures must not reset: %v", a.Failures)
	}
}

func TestLoginFailureKeepsSnapshot(t *testing.T) {
	clock := &fakeClock{now: base}
	source := &fakeSource{created: map[string]time.Time{"a": base}}
	c := newCollector(source, clock, map[string]time.Duration{"a": time.Hour})
	c.Collect(t.Context())

	clock.Advance(5 * time.Minute)
	source.set(func() { source.loginErr = down })
	c.Collect(t.Context())

	s := c.Snapshot()
	if s.Success {
		t.Fatal("collection should have failed")
	}
	if !s.LastSuccess.Equal(base) {
		t.Fatalf("last success moved: %s", s.LastSuccess)
	}
	if s.Failures[openbao.ReasonUnavailable] != 1 {
		t.Fatalf("failures = %v", s.Failures)
	}
	if a := keyStatus(t, s, "a"); !a.Up || !a.Due.Equal(base.Add(time.Hour)) {
		t.Fatalf("a changed: %+v", a)
	}
}

func TestAllKeysFailedIsACollectionFailure(t *testing.T) {
	clock := &fakeClock{now: base}
	source := &fakeSource{created: map[string]time.Time{"a": base}}
	c := newCollector(source, clock, map[string]time.Duration{"a": time.Hour})
	c.Collect(t.Context())

	clock.Advance(5 * time.Minute)
	source.set(func() { source.errs = map[string]error{"a": down} })
	c.Collect(t.Context())

	s := c.Snapshot()
	if s.Success || !s.LastSuccess.Equal(base) {
		t.Fatalf("unexpected state: %+v", s)
	}
	if s.Failures[allKeysFailed] != 1 {
		t.Fatalf("failures = %v", s.Failures)
	}
	if a := keyStatus(t, s, "a"); a.Up || !a.Due.Equal(base.Add(time.Hour)) {
		t.Fatalf("a = %+v", a)
	}
}

func TestFirstCollectionFails(t *testing.T) {
	source := &fakeSource{loginErr: down}
	c := newCollector(source, &fakeClock{now: base}, map[string]time.Duration{"a": time.Hour})
	c.Collect(t.Context())

	s := c.Snapshot()
	if !s.Attempted || s.Success || !s.LastSuccess.IsZero() {
		t.Fatalf("unexpected state: %+v", s)
	}
	if a := keyStatus(t, s, "a"); a.Up || !a.Due.IsZero() {
		t.Fatalf("a = %+v", a)
	}
}

func TestCollectSettlesAllReadsBeforePublishing(t *testing.T) {
	clock := &fakeClock{now: base}
	keys := map[string]time.Duration{"fails": time.Hour, "slow": time.Hour}
	release := make(chan struct{})
	failed := make(chan struct{})
	source := &fakeSource{
		created: map[string]time.Time{"slow": base},
		errs:    map[string]error{"fails": forbid},
	}
	source.onRead = func(_ context.Context, key string) {
		switch key {
		case "fails":
			close(failed)
		case "slow":
			<-failed
			<-release
		}
	}
	c := newCollector(source, clock, keys)

	done := make(chan struct{})
	go func() {
		defer close(done)
		c.Collect(t.Context())
	}()
	<-failed
	if c.Snapshot().Attempted {
		t.Fatal("published before every read settled")
	}
	close(release)
	<-done

	s := c.Snapshot()
	if got := keyStatus(t, s, "slow"); !got.Up {
		t.Fatalf("slow read was lost: %+v", got)
	}
	if got := keyStatus(t, s, "fails"); got.Up {
		t.Fatalf("fails should be down: %+v", got)
	}
}

func TestSnapshotsAreImmutable(t *testing.T) {
	source := &fakeSource{errs: map[string]error{"a": forbid}}
	c := newCollector(source, &fakeClock{now: base}, map[string]time.Duration{"a": time.Hour})
	c.Collect(t.Context())
	first := c.Snapshot()
	firstFailures := maps.Clone(first.Keys[0].Failures)

	c.Collect(t.Context())

	if !maps.Equal(first.Keys[0].Failures, firstFailures) {
		t.Fatalf("an earlier snapshot changed: %v", first.Keys[0].Failures)
	}
	if got := c.Snapshot().Keys[0].Failures[openbao.ReasonForbidden]; got != 2 {
		t.Fatalf("failures = %d", got)
	}
}

func TestConcurrencyIsBounded(t *testing.T) {
	var active, peak atomic.Int32
	source := &fakeSource{created: map[string]time.Time{}}
	source.onRead = func(context.Context, string) {
		now := active.Add(1)
		for {
			old := peak.Load()
			if now <= old || peak.CompareAndSwap(old, now) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		active.Add(-1)
	}
	keys := map[string]time.Duration{}
	for _, key := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		keys[key] = time.Hour
	}
	c := newCollector(source, &fakeClock{now: base}, keys)
	c.Collect(t.Context())
	if got := peak.Load(); got > 2 {
		t.Fatalf("peak concurrency = %d", got)
	}
}

func TestCanceledCollectionIsNotPublished(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	source := &fakeSource{}
	source.onRead = func(context.Context, string) { cancel() }
	c := newCollector(source, &fakeClock{now: base}, map[string]time.Duration{"a": time.Hour})

	c.Collect(ctx)

	if c.Snapshot().Attempted {
		t.Fatal("a canceled collection was published")
	}
}

func TestCollectionTimeoutReachesSource(t *testing.T) {
	source := &fakeSource{}
	var hasDeadline atomic.Bool
	source.onRead = func(ctx context.Context, _ string) {
		_, ok := ctx.Deadline()
		hasDeadline.Store(ok)
	}
	c := newCollector(source, &fakeClock{now: base}, map[string]time.Duration{"a": time.Hour})
	c.Collect(t.Context())
	if !hasDeadline.Load() {
		t.Fatal("reads have no deadline")
	}
}

func TestRunCollectsOnEveryInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var reads atomic.Int32
		source := &fakeSource{}
		source.onRead = func(context.Context, string) { reads.Add(1) }
		c := New(source, Options{
			Keys:        map[string]time.Duration{"a": time.Hour},
			Interval:    5 * time.Minute,
			Timeout:     time.Minute,
			Concurrency: 1,
			Now:         time.Now,
			Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		})
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() {
			defer close(done)
			c.Run(ctx)
		}()

		synctest.Wait()
		if got := reads.Load(); got != 1 {
			t.Fatalf("reads after start = %d, want an immediate collection", got)
		}
		time.Sleep(10*time.Minute + time.Second)
		synctest.Wait()
		if got := reads.Load(); got != 3 {
			t.Fatalf("reads after 10m = %d", got)
		}

		cancel()
		<-done
	})
}
