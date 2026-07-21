// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"fmt"
	"io"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
)

// graphOptions wires the Microsoft Graph (Outlook/M365) capture surface: the
// OAuth connect/callback transport (which rides the vault, so the caller
// appends these AFTER keyvault options, and after gmailOptions so both
// providers share one connect registry). WithGraphCapture self-gates: absent
// the client id/secret, state key, or public base URL it is a no-op and
// /connectors/graph/* keeps its declared 501.
func graphOptions(cfg apiConfig, pool *pgxpool.Pool, logger *slog.Logger, stdout io.Writer) ([]compose.Option, error) {
	graphCfg := compose.GraphConfig{
		ClientID:      cfg.graphClientID,
		ClientSecret:  cfg.graphClientSecret,
		Tenant:        cfg.graphTenant,
		StateKey:      cfg.connectorStateKey,
		PublicBaseURL: cfg.publicBaseURL,
		APIBaseURL:    cfg.apiBaseURL,
	}
	opts := []compose.Option{compose.WithGraphCapture(graphCfg)}
	switch {
	case graphCfg.Enabled():
		// The backfill ops ride the shared connect registry plus an
		// insert-only client — the api enqueues the paging job, the worker
		// pages. WithCaptureBackfill is idempotent, so a deployment that
		// already mounted it for gmail keeps that wiring.
		backfillInserter, err := jobs.NewInserter(pool, logger)
		if err != nil {
			return nil, err
		}
		opts = append(opts, compose.WithCaptureBackfill(backfillInserter))
		_, _ = fmt.Fprintln(stdout, "api graph capture connector enabled (/connectors/graph/*, backfill ops)")
	case cfg.graphClientID != "":
		_, _ = fmt.Fprintln(stdout, "api graph capture connector configured but INCOMPLETE — needs client secret, --connector-state-key (>=32B), and --public-base-url; surface stays 501")
	}
	return opts, nil
}
