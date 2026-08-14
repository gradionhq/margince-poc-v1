// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The boot-time reconcile for the derived channel vocabulary (DESIGN-SP4 §4):
// the ONE place activity_kind and channel_provider are written, and the ONE
// place activities.SetChannelProviders and comms.SetChannelProviders are
// called — so the DB registry and both packages' in-memory snapshots cannot
// be set two different ways.
//
// Called from NewCaptureRegistry, which this codebase already constructs more
// than once per process (a role-specific alternate wiring path, the worker's
// one-shot backfill helper) — so every write here is an idempotent upsert, and
// every in-memory set is last-write-wins, never a once-only registration.

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/comms"
	"github.com/gradionhq/margince/backend/internal/platform/database"
)

// reconcileChannelProviders inserts an activity_kind row and a matching
// channel_provider row (transport='core') for every name in providers not
// already present, then sets both packages' in-memory snapshots to exactly
// the composed set passed in.
//
// It runs over database.WithInfraTx, not the workspace-bound database.DB.Tx:
// activity_kind and channel_provider carry no workspace_id, so binding a
// tenant GUC would ask a question these tables have no answer to — and
// database.DB.Tx's workspace resolution fails outright on a fresh install
// with no organization bootstrapped yet, which is exactly when a process
// first constructs this registry.
//
// activity_kind is inserted FIRST, in the same transaction: channel_provider
// FKs into it, so a provider this binary just compiled in but that the core
// migration's fixed seed never anticipated (a future core channel connector
// beyond telegram) would otherwise fail this very insert with a foreign-key
// violation — the exact deployment fact this reconcile exists to establish,
// self-inflicted.
//
// It never DELETEs. A provider whose supplier is gone on a later boot keeps
// its row — activity and person_channel_identity rows still reference it, the
// FK would refuse the delete anyway, and ErrConnectorNotConfigured already
// parks a send against it rather than needing the row gone.
func reconcileChannelProviders(ctx context.Context, pool *pgxpool.Pool, providers []string) error {
	err := database.WithInfraTx(ctx, pool, func(tx pgx.Tx) error {
		for _, provider := range providers {
			if _, err := tx.Exec(ctx, `
				INSERT INTO activity_kind (kind) VALUES ($1)
				ON CONFLICT (kind) DO NOTHING`, provider); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO channel_provider (provider, transport) VALUES ($1, 'core')
				ON CONFLICT (provider) DO NOTHING`, provider); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	activities.SetChannelProviders(providers)
	comms.SetChannelProviders(providers)
	return nil
}
