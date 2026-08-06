// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package events

// Real-Redis lane for the reset's bus purge: entries go, consumer groups
// come back, and nothing outside the declared key inventory is touched.

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	kevents "github.com/gradionhq/margince/backend/internal/shared/kernel/events"
)

// purgeTestRedis mirrors bus_integration_test.go's setup: a per-test Redis
// db (never db 0, which a running `make dev` owns), flushed clean before
// the test runs so purge assertions never inherit another test's keys.
func purgeTestRedis(t *testing.T) (context.Context, *redis.Client) {
	t.Helper()
	redisAddr := os.Getenv("MARGINCE_TEST_REDIS")
	if redisAddr == "" {
		t.Fatal("MARGINCE_TEST_REDIS not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	ctx := t.Context()

	rdb := redis.NewClient(&redis.Options{Addr: redisAddr, DB: testRedisDB(t)})
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Fatalf("redis at %s unreachable — run `make db-up`: %v", redisAddr, err)
	}
	if err := rdb.FlushDB(ctx).Err(); err != nil {
		t.Fatalf("flushing test redis db: %v", err)
	}
	t.Cleanup(func() {
		if err := rdb.Close(); err != nil {
			t.Errorf("closing redis client: %v", err)
		}
	})

	return ctx, rdb
}

func TestPurgeStreamsEmptiesEveryCatalogStreamAndRestoresEveryGroup(t *testing.T) {
	ctx, rdb := purgeTestRedis(t)
	for _, stream := range kevents.Streams() {
		if err := rdb.XAdd(ctx, &redis.XAddArgs{Stream: stream, Values: map[string]any{"v": "{}"}}).Err(); err != nil {
			t.Fatalf("seeding %s: %v", stream, err)
		}
	}

	deleted, err := PurgeStreams(ctx, rdb, kevents.Groups())
	if err != nil {
		t.Fatalf("PurgeStreams: %v", err)
	}
	if deleted != len(kevents.Streams()) {
		t.Errorf("deleted = %d stream keys, want %d", deleted, len(kevents.Streams()))
	}

	// Derived from the catalog, not a hand-kept list: a newly declared stream
	// or group is covered by this assertion the moment it is added.
	for _, stream := range kevents.Streams() {
		n, err := rdb.XLen(ctx, stream).Result()
		if err != nil {
			t.Fatalf("XLEN %s: %v", stream, err)
		}
		if n != 0 {
			t.Errorf("%s still holds %d entries after the purge", stream, n)
		}
	}
	for _, g := range kevents.Groups() {
		for _, stream := range g.Streams {
			groups, err := rdb.XInfoGroups(ctx, stream).Result()
			if err != nil {
				t.Fatalf("XINFO GROUPS %s: %v", stream, err)
			}
			if !hasGroup(groups, g.Name) {
				t.Errorf("group %s is gone from %s; a live subscriber would wedge on NOGROUP", g.Name, stream)
			}
		}
	}
}

func TestPurgeDedupeDeletesOnlyDedupeKeys(t *testing.T) {
	ctx, rdb := purgeTestRedis(t)
	if err := rdb.Set(ctx, DedupeKeyPrefix+"cg:probe:abc", "1", time.Minute).Err(); err != nil {
		t.Fatalf("seeding a dedupe key: %v", err)
	}
	if err := rdb.Set(ctx, "unrelated:key", "1", time.Minute).Err(); err != nil {
		t.Fatalf("seeding an unrelated key: %v", err)
	}

	deleted, err := PurgeDedupe(ctx, rdb)
	if err != nil {
		t.Fatalf("PurgeDedupe: %v", err)
	}
	if deleted != 1 {
		t.Errorf("deleted = %d, want 1", deleted)
	}
	if n, err := rdb.Exists(ctx, DedupeKeyPrefix+"cg:probe:abc").Result(); err != nil || n != 0 {
		t.Errorf("the dedupe key survived the purge (exists=%d, err=%v); PurgeDedupe must actually UNLINK it, not just count it", n, err)
	}
	if n, err := rdb.Exists(ctx, "unrelated:key").Result(); err != nil || n != 1 {
		t.Errorf("an unrelated key was collateral damage (exists=%d, err=%v); the purge works from a declared inventory, never FLUSHDB", n, err)
	}
}

// TestPurgeDedupeFlushesMidScan seeds one full scanBatch plus a partial tail,
// forcing PurgeDedupe through its mid-scan flush (len(batch) == scanBatch) as
// well as the final flush after the loop — a count that survives both is the
// only way to know the batching itself drops no keys.
func TestPurgeDedupeFlushesMidScan(t *testing.T) {
	ctx, rdb := purgeTestRedis(t)
	const seeded = scanBatch + 1

	pipe := rdb.Pipeline()
	for i := 0; i < seeded; i++ {
		pipe.Set(ctx, fmt.Sprintf("%scg:probe:%d", DedupeKeyPrefix, i), "1", time.Minute)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		t.Fatalf("seeding %d dedupe keys: %v", seeded, err)
	}

	deleted, err := PurgeDedupe(ctx, rdb)
	if err != nil {
		t.Fatalf("PurgeDedupe: %v", err)
	}
	if deleted != seeded {
		t.Errorf("deleted = %d, want %d", deleted, seeded)
	}

	remaining, err := rdb.Keys(ctx, DedupeKeyPrefix+"*").Result()
	if err != nil {
		t.Fatalf("KEYS %s*: %v", DedupeKeyPrefix, err)
	}
	if len(remaining) != 0 {
		t.Errorf("%d dedupe keys survived the purge, want 0", len(remaining))
	}
}

func hasGroup(groups []redis.XInfoGroup, name string) bool {
	for _, g := range groups {
		if g.Name == name {
			return true
		}
	}
	return false
}
