// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package events

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"

	kevents "github.com/gradionhq/margince/backend/internal/shared/kernel/events"
)

// scanBatch bounds one SCAN round trip. SCAN is incremental by contract, so
// this is a throughput knob, never a completeness one.
const scanBatch = 500

// PurgeStreams drops every catalog stream and re-creates the consumer groups
// on top of the empty streams. It returns the number of stream KEYS deleted
// (DEL reports keys; the entry count is unbounded and tells an operator
// nothing).
//
// The cut is installation-wide rather than per-workspace, which is exact under
// A107/ADR-0061 — one installation serves one organization, so every entry in
// these streams belongs to the workspace being reset — and O(1) instead of
// scanning up to 131072 entries per stream.
//
// Re-creating the groups is not optional. Dropping a stream key takes its
// groups with it, and a live subscriber would then log NOGROUP once a second
// forever; the subscriber's own recovery covers the window between the two
// calls.
//
// UNLINK rather than DEL, for the reason PurgeDedupe gives: a stream retains up
// to its MAXLEN in entries, and DEL reclaims every one of them synchronously on
// the command path. On a busy installation that stalls the Redis instance —
// which a reset shares with whatever else is running against it.
func PurgeStreams(ctx context.Context, rdb *redis.Client, groups []kevents.Group) (int, error) {
	deleted, err := rdb.Unlink(ctx, kevents.Streams()...).Result()
	if err != nil {
		return 0, fmt.Errorf("bus: purging streams: %w", err)
	}
	for _, g := range groups {
		for _, stream := range g.Streams {
			err := rdb.XGroupCreateMkStream(ctx, stream, g.Name, "0").Err()
			if err != nil && !isBusyGroup(err) {
				return int(deleted), fmt.Errorf("bus: restoring group %s on %s: %w", g.Name, stream, err)
			}
		}
	}
	return int(deleted), nil
}

// PurgeDedupe removes every processed-event mark, returning the number of keys
// unlinked. The marks carry no workspace segment, so the whole namespace goes:
// under one-installation-one-organization that IS this tenant's set, and a
// stale mark left behind would swallow a redelivery the reseeded install
// should see.
//
// UNLINK rather than DEL: reclamation happens off the command path, and a
// reset can face a namespace with a 96h TTL horizon behind it.
func PurgeDedupe(ctx context.Context, rdb *redis.Client) (int, error) {
	var deleted int
	iter := rdb.Scan(ctx, 0, DedupeKeyPrefix+"*", scanBatch).Iterator()
	batch := make([]string, 0, scanBatch)
	for iter.Next(ctx) {
		batch = append(batch, iter.Val())
		if len(batch) == scanBatch {
			n, err := unlink(ctx, rdb, batch)
			if err != nil {
				return deleted, err
			}
			deleted += n
			batch = batch[:0]
		}
	}
	if err := iter.Err(); err != nil {
		return deleted, fmt.Errorf("bus: scanning dedupe keys: %w", err)
	}
	n, err := unlink(ctx, rdb, batch)
	if err != nil {
		return deleted, err
	}
	return deleted + n, nil
}

// unlink removes one batch of keys, tolerating an empty batch so callers do
// not have to guard the tail of a scan.
func unlink(ctx context.Context, rdb *redis.Client, keys []string) (int, error) {
	if len(keys) == 0 {
		return 0, nil
	}
	n, err := rdb.Unlink(ctx, keys...).Result()
	if err != nil {
		return 0, fmt.Errorf("bus: unlinking %d keys: %w", len(keys), err)
	}
	return int(n), nil
}
