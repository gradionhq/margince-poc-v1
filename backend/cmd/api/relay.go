// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/platform/events"
	kevents "github.com/gradionhq/margince/backend/internal/shared/kernel/events"
)

// startInlineRelay boots the in-process outbox relay. The bus is not
// optional plumbing: without a relay every committed write strands its
// outbox row, so an unreachable Redis fails the boot the same way an
// unreachable Postgres does (B-EP04.1). The returned compose option makes
// the bus a readiness dependency of THIS process (a split deployment's
// api is ready on Postgres alone); the stop function runs after the HTTP
// server shuts down, so late-committing requests usually ship before
// exit — anything still unshipped waits durably in the outbox for the
// next boot, and shutdown loses no events.
//
//nolint:contextcheck // the relay + webhook consumer are process-lifetime lanes, deliberately rooted at context.Background() and stopped by the returned stop(), never by the request ctx.
func startInlineRelay(ctx context.Context, pool *pgxpool.Pool, redisAddr, webhookKey string, webhookRetryInterval time.Duration, logger *slog.Logger) (compose.Option, func(), error) {
	rdb, err := events.NewClient(ctx, redisAddr)
	if err != nil {
		return nil, nil, err
	}
	// The relay/consumer lanes outlive any single request by design — a bus
	// lane must drain on shutdown, not cancel with an inbound request — so
	// they run on a fresh cancelable context, not the request ctx.
	relayCtx, cancel := context.WithCancel(context.Background())
	var relay sync.WaitGroup
	relay.Go(func() {
		events.NewRelay(pool, rdb, logger).Run(relayCtx)
	})
	// When a webhook signing key is configured, this single-process role
	// also runs the cg:webhooks delivery consumer + retry sweep (in a split
	// deployment cmd/worker owns them). Owner-scoped fan-out (BYO-EVT-4)
	// rides the same deliverer.
	if webhookKey != "" {
		if derr := startInlineWebhookDelivery(relayCtx, &relay, rdb, pool, webhookKey, webhookRetryInterval, logger); derr != nil {
			cancel()
			if cerr := rdb.Close(); cerr != nil {
				logger.Warn("closing bus client", "err", cerr)
			}
			return nil, nil, fmt.Errorf("api: %w", derr)
		}
	}
	stop := func() {
		cancel()
		relay.Wait()
		if err := rdb.Close(); err != nil {
			logger.Warn("closing bus client", "err", err)
		}
	}
	busReady := compose.WithBusReady(func(ctx context.Context) error {
		return rdb.Ping(ctx).Err()
	})
	return busReady, stop, nil
}

// startInlineWebhookDelivery builds the owner-scoped delivery deliverer and
// registers its cg:webhooks consumer + retry sweep on the relay group. Kept
// out of startInlineRelay so that function stays flat; both goroutines share
// the relay's lifecycle context and WaitGroup.
func startInlineWebhookDelivery(ctx context.Context, relay *sync.WaitGroup, rdb *redis.Client, pool *pgxpool.Pool, webhookKey string, retryInterval time.Duration, logger *slog.Logger) error {
	deliverer, err := compose.NewWebhookDeliverer(pool, webhookKey, logger)
	if err != nil {
		return err
	}
	var group kevents.Group
	for _, g := range kevents.Groups() {
		if g.Name == "cg:webhooks" {
			group = g
		}
	}
	relay.Go(func() {
		sub := events.NewSubscriber(rdb, group, events.Dedupe(rdb, group.Name, deliverer.HandleEvent), logger)
		if err := sub.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("subscriber cg:webhooks", "err", err)
		}
	})
	relay.Go(func() { deliverer.RunRetrySweep(ctx, retryInterval) })
	return nil
}
