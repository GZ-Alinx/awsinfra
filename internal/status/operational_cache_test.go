package status

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestOperationalCacheCoalescesConcurrentLoadsAndInvalidatesTarget(t *testing.T) {
	cache := newOperationalCache()
	key := operationalCacheKey("kbp-test", "services")
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	loader := func() (string, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return "ready", nil
	}

	const readers = 8
	results := make(chan string, readers)
	errors := make(chan error, readers)
	var wait sync.WaitGroup
	for range readers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			value, err := loadOperationalValue(context.Background(), cache, key, time.Minute, loader)
			results <- value
			errors <- err
		}()
	}
	<-started
	close(release)
	wait.Wait()
	close(results)
	close(errors)

	for err := range errors {
		if err != nil {
			t.Fatalf("coalesced load failed: %v", err)
		}
	}
	for value := range results {
		if value != "ready" {
			t.Fatalf("coalesced value = %q", value)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("loader calls = %d, want 1", calls.Load())
	}

	if _, err := loadOperationalValue(context.Background(), cache, key, time.Minute, loader); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("cache hit launched loader: calls=%d", calls.Load())
	}

	cache.deleteTarget("kbp-test")
	release = make(chan struct{})
	close(release)
	if _, err := loadOperationalValue(context.Background(), cache, key, time.Minute, loader); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("target invalidation did not force reload: calls=%d", calls.Load())
	}
}

func TestOperationalCacheWaiterHonorsCancellation(t *testing.T) {
	cache := newOperationalCache()
	key := operationalCacheKey("kbp-test", "ingresses")
	started := make(chan struct{})
	release := make(chan struct{})
	go func() {
		_, _ = loadOperationalValue(context.Background(), cache, key, time.Minute, func() (string, error) {
			close(started)
			<-release
			return "ready", nil
		})
	}()
	<-started

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := loadOperationalValue(ctx, cache, key, time.Minute, func() (string, error) {
		return "unexpected", nil
	}); err == nil {
		t.Fatal("canceled waiter was not released")
	}
	close(release)
}
