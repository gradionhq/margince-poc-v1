// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// Wiring the person auto-enrich consumer.
//
// It is a consumer rather than a call inside any writer, and the reason is the
// one linkedinmatchgen.go already argued: matching only at write time means
// every later arrival is a match nobody will ever make. person.created reaches
// the outbox through the write shape, so one subscriber covers manual entry,
// capture, site read, merge and import at once — and covers any writer added
// later without that writer knowing it exists.

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
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/websearchhttp"
)

// startPersonAutoEnrich subscribes the consumer that fills a contact from what
// their employer's site already published, and from public search metadata when
// a provider is bound.
func startPersonAutoEnrich(
	ctx context.Context,
	pool *pgxpool.Pool,
	rdb *redis.Client,
	background *sync.WaitGroup,
	logger *slog.Logger,
	stdout io.Writer,
) {
	// Search is optional by design (ADR-0081): a deployment that binds no
	// provider fills from the employer's own pages and skips discovery,
	// which is the sovereign posture rather than a degraded one.
	searchClient, searchConfigured := websearchhttp.FromEnv(time.Now)
	enricher := compose.NewPersonAutoEnrich(pool, people.NewStore(pool), approvals.NewService(pool), searchClient, logger)
	if searchConfigured {
		_, _ = fmt.Fprintln(stdout, "worker filling contacts from their employer's pages and public search results")
	} else {
		_, _ = fmt.Fprintln(stdout, "worker filling contacts from their employer's published pages (no search provider bound)")
	}
	background.Go(func() { runSubscriber(ctx, rdb, "cg:person-auto-enrich", enricher.HandleEvent, logger, 0) })
}
