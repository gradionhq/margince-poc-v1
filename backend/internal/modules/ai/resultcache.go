// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"sync"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// resultCache is the §6 result cache: workspace_id is part of the key
// (RT-AI-M7 — two tenants with identical inputs must never share an
// answer), TTL-bounded, with a per-workspace invalidation hook.
type resultCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	now     func() time.Time
	entries map[string]cacheEntry
}

type cacheEntry struct {
	workspaceID ids.WorkspaceID
	resp        model.Response
	tier        Tier
	expires     time.Time
}

// maxResultCacheEntries bounds resident memory: expired entries are only
// reaped lazily on same-key reads, so without a cap a stream of unique
// requests would leave dead entries resident for the life of the process.
const maxResultCacheEntries = 1024

func newResultCache(ttl time.Duration) *resultCache {
	return &resultCache{ttl: ttl, now: time.Now, entries: map[string]cacheEntry{}}
}

func (c *resultCache) get(key string, wsID ids.WorkspaceID) (model.Response, Tier, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok || c.now().After(entry.expires) {
		delete(c.entries, key)
		return model.Response{}, "", false
	}
	// Defense in depth for RT-AI-M7: even a corrupted key can never
	// serve another workspace's answer.
	if entry.workspaceID != wsID {
		return model.Response{}, "", false
	}
	return entry.resp, entry.tier, true
}

func (c *resultCache) put(key string, wsID ids.WorkspaceID, resp model.Response, tier Tier) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[key]; !exists && len(c.entries) >= maxResultCacheEntries {
		c.makeRoomLocked()
	}
	c.entries[key] = cacheEntry{workspaceID: wsID, resp: resp, tier: tier, expires: c.now().Add(c.ttl)}
}

// makeRoomLocked frees at least one slot: first a full sweep of expired
// entries (the only global reap — get only deletes the key it reads),
// then, if every entry is still live, the soonest-to-expire one goes —
// it holds the least remaining TTL value.
func (c *resultCache) makeRoomLocked() {
	now := c.now()
	for key, entry := range c.entries {
		if now.After(entry.expires) {
			delete(c.entries, key)
		}
	}
	if len(c.entries) < maxResultCacheEntries {
		return
	}
	var soonestKey string
	var soonest time.Time
	for key, entry := range c.entries {
		if soonestKey == "" || entry.expires.Before(soonest) {
			soonestKey, soonest = key, entry.expires
		}
	}
	delete(c.entries, soonestKey)
}

func (c *resultCache) invalidate(wsID ids.WorkspaceID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, entry := range c.entries {
		if entry.workspaceID == wsID {
			delete(c.entries, key)
		}
	}
}
