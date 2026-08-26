package status

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

// operationalCache keeps only short-lived, non-secret EKS observations in
// process. It also coalesces identical in-flight requests so several browser
// tabs cannot fan out into the same AWS CLI and kubectl calls.
type operationalCache struct {
	mu      sync.Mutex
	entries map[string]operationalCacheEntry
	calls   map[string]*operationalCacheCall
}

type operationalCacheEntry struct {
	value     any
	expiresAt time.Time
}

type operationalCacheCall struct {
	done  chan struct{}
	value any
	err   error
}

func newOperationalCache() *operationalCache {
	return &operationalCache{
		entries: make(map[string]operationalCacheEntry),
		calls:   make(map[string]*operationalCacheCall),
	}
}

func operationalCacheKey(target, resource string) string {
	return strings.TrimSpace(target) + "\x00" + resource
}

func loadOperationalValue[T any](ctx context.Context, cache *operationalCache, key string, ttl time.Duration, loader func() (T, error)) (T, error) {
	var zero T
	if cache == nil || ttl <= 0 {
		return loader()
	}
	now := time.Now()
	cache.mu.Lock()
	if entry, ok := cache.entries[key]; ok && now.Before(entry.expiresAt) {
		value, valid := entry.value.(T)
		if valid {
			cache.mu.Unlock()
			return value, nil
		}
		delete(cache.entries, key)
	}
	if call, ok := cache.calls[key]; ok {
		cache.mu.Unlock()
		select {
		case <-ctx.Done():
			return zero, ctx.Err()
		case <-call.done:
			if call.err != nil {
				return zero, call.err
			}
			value, valid := call.value.(T)
			if !valid {
				return zero, errors.New("operational cache returned an unexpected value type")
			}
			return value, nil
		}
	}
	call := &operationalCacheCall{done: make(chan struct{})}
	cache.calls[key] = call
	cache.mu.Unlock()

	value, err := loader()

	cache.mu.Lock()
	call.value, call.err = value, err
	if err == nil {
		cache.entries[key] = operationalCacheEntry{value: value, expiresAt: time.Now().Add(ttl)}
	}
	delete(cache.calls, key)
	close(call.done)
	cache.mu.Unlock()
	return value, err
}

func (c *operationalCache) deleteTarget(target string) {
	if c == nil {
		return
	}
	prefix := strings.TrimSpace(target) + "\x00"
	c.mu.Lock()
	for key := range c.entries {
		if strings.HasPrefix(key, prefix) {
			delete(c.entries, key)
		}
	}
	c.mu.Unlock()
}

func (c *operationalCache) delete(target, resource string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	delete(c.entries, operationalCacheKey(target, resource))
	c.mu.Unlock()
}
