// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package events

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/redis/go-redis/v9"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// ResetChannel carries data-reset notifications to every running process.
//
// Pub/sub, not the outbox bus: this is a process-lifecycle signal, not a
// domain event. It has no envelope, no audit trail and no replay, and it must
// NOT enter the spec-pinned event catalog — a subscriber that missed it has a
// stale cache entry that its own TTL retires, whereas a domain event that
// went missing would be a lost fact.
const ResetChannel = "gw:control:reset"

// PublishReset announces that ws was reset, so every process drops what it
// cached for that workspace. Delivery is best-effort by construction: pub/sub
// reaches whoever is listening now, which is precisely the set of processes
// that hold caches.
func PublishReset(ctx context.Context, rdb *redis.Client, ws ids.UUID) error {
	if err := rdb.Publish(ctx, ResetChannel, ws.String()).Err(); err != nil {
		return fmt.Errorf("bus: announcing the reset of workspace %s: %w", ws, err)
	}
	return nil
}

// SubscribeReset runs until ctx is canceled, invoking fn for every reset
// announcement. A malformed payload is logged by the caller's fn only if it
// parses; an unparseable one is skipped rather than killing the loop, because
// a control channel that dies on one bad message stops flushing caches for
// the life of the process.
func SubscribeReset(ctx context.Context, rdb *redis.Client, log *slog.Logger, fn func(ids.UUID)) error {
	return subscribeResetWithReady(ctx, rdb, log, fn, nil)
}

func subscribeResetWithReady(ctx context.Context, rdb *redis.Client, log *slog.Logger, fn func(ids.UUID), ready chan<- struct{}) error {
	sub := rdb.Subscribe(ctx, ResetChannel)
	defer func() {
		if err := sub.Close(); err != nil && ctx.Err() == nil {
			log.Error("bus: closing the reset control subscription", "error", err)
		}
	}()
	if _, err := sub.Receive(ctx); err != nil {
		return fmt.Errorf("bus: subscribing to %s: %w", ResetChannel, err)
	}
	if ready != nil {
		close(ready)
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg, ok := <-sub.Channel():
			if !ok {
				return nil
			}
			ws, err := ids.Parse(msg.Payload)
			if err != nil {
				continue
			}
			fn(ws)
		}
	}
}
