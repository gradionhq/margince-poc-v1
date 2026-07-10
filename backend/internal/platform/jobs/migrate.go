// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package jobs

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

// grantRiverTablesToApp gives the runtime application role the privileges
// it needs on River's tables and sequences. River owns its own schema
// (ADR-0017: a self-contained namespace with its own migrator), so its
// tables are created after the core grant migration and are not covered by
// its ALTER DEFAULT PRIVILEGES — and river_job's bigserial id needs an
// explicit sequence grant the core UUID-keyed tables never required. The
// block is conditional on the role existing, mirroring core migration
// 0015, so throwaway databases that run everything as the owner apply the
// same schema. It deliberately touches only river_* objects: a blanket
// GRANT would re-grant UPDATE/DELETE on audit_log, undoing 0015's
// append-only revoke.
const grantRiverTablesToApp = `
DO $$
DECLARE r record;
BEGIN
	IF EXISTS (SELECT FROM pg_roles WHERE rolname = 'margince_app') THEN
		FOR r IN SELECT tablename FROM pg_tables
			WHERE schemaname = 'public' AND tablename LIKE 'river_%' LOOP
			EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON public.%I TO margince_app', r.tablename);
		END LOOP;
		FOR r IN SELECT sequencename FROM pg_sequences
			WHERE schemaname = 'public' AND sequencename LIKE 'river_%' LOOP
			EXECUTE format('GRANT USAGE, SELECT ON SEQUENCE public.%I TO margince_app', r.sequencename);
		END LOOP;
	END IF;
END $$;`

// Migrate applies River's own schema (river_job, river_leader, …) through
// its migrator, then grants the runtime role access to it. It runs as the
// fourth namespace step of cmd/migrate, after core and custom, with the
// owner-role pool. Idempotent: a second run applies nothing and re-grants
// harmlessly.
func Migrate(ctx context.Context, pool *pgxpool.Pool) (applied int, err error) {
	migrator, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		return 0, fmt.Errorf("jobs: river migrator: %w", err)
	}
	res, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil)
	if err != nil {
		return 0, fmt.Errorf("jobs: river migrate up: %w", err)
	}
	if _, err := pool.Exec(ctx, grantRiverTablesToApp); err != nil {
		return 0, fmt.Errorf("jobs: granting river tables to app role: %w", err)
	}
	return len(res.Versions), nil
}
