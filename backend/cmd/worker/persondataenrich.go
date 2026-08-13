// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// Wiring the automatic provider-enrichment consumer (ADR-0101/PI-EVT-1).
//
// A consumer rather than a call inside any writer, for the reason the
// auto-enrich lane beside it already argues: person.created reaches the outbox
// through the write shape, so one subscriber covers manual entry, capture,
// import and the site read at once, and covers any writer added later without
// that writer knowing it exists.
//
// It runs in the WORKER because that is where the run is executed. The api
// queues on a human's explicit request; this queues on the customer's standing
// instruction, and both land in the same table for the same worker to drain.

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/integrations"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
)

// startPersonDataEnrich subscribes the consumer that buys a newly created
// person's data from the connected provider.
//
// Without a registry or a vault there is nothing to subscribe: no adapter can
// be reached and no credential can be unsealed, so the lane stays absent
// rather than running to refuse every event (PI-AC-9).
func startPersonDataEnrich(
	ctx context.Context,
	pool *pgxpool.Pool,
	rdb *redis.Client,
	registry *integrations.Registry,
	vault keyvault.Vault,
	background *sync.WaitGroup,
	logger *slog.Logger,
	stdout io.Writer,
) error {
	if registry == nil || vault == nil {
		return nil
	}
	// Insert-only, like every other enqueuing lane in this role: the submit
	// job this queues is worked by this process's own River runner, and a
	// lane that staged onto the runner it is being wired into would need the
	// runner to exist before the lanes do.
	inserter, err := jobs.NewInserter(pool, logger)
	if err != nil {
		return fmt.Errorf("worker: the provider submit inserter: %w", err)
	}
	store, err := compose.NewProviderRunService(pool, registry, vault, inserter, time.Now)
	if err != nil {
		return fmt.Errorf("worker: the provider run service: %w", err)
	}
	consumer := compose.NewPersonDataEnrich(pool, store, logger)
	_, _ = fmt.Fprintln(stdout, "worker enriching newly created contacts from the connected data provider (cg:person-data)")
	background.Go(func() { runSubscriber(ctx, rdb, "cg:person-data", consumer.HandleEvent, logger, 0) })
	return nil
}
