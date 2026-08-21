// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package testdb

import (
	"context"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/dbmigrate"
	"github.com/gradionhq/margince/backend/migrations"
)

// EnsureSchema brings the test database to this binary's head exactly once per
// process. Every later test in the same process is a no-op here and resets via
// Reset. Any caller may pass any owner connection to the same database — the
// work runs on whichever connection wins the race to the sync.Once, and the
// result is the same schema for all of them.
//
// The lane hands most processes a database that is ALREADY at head: each
// package gets a CREATE DATABASE ... TEMPLATE copy of the migrated
// margince_test (scripts/lib-testdb.sh), and dropping that schema to rebuild it
// from the same migrations cost ~1.3 s per package process — attributed by
// go test to whichever test happened to run first. reusableClone establishes
// when the copy can be taken at its word instead; see CloneDBEnv for what has
// to be true, and why every one of those conditions is load-bearing rather than
// defensive.
func EnsureSchema(ctx context.Context, owner *pgx.Conn) error {
	migrateOnce.Do(func() {
		reusable, reason, err := reusableClone(ctx, owner)
		if err != nil {
			migrateErr = err
			return
		}
		if !reusable {
			if reason != "" {
				// Only a caller that DECLARED a clone and was refused gets a
				// line. Silence here means the caller never asked for the
				// skip, which is the ordinary case for the serial lane and for
				// a suite that migrates a database it made itself.
				fmt.Fprintf(os.Stderr, "testdb: rebuilding the schema rather than reusing the clone — %s\n", reason)
			}
			if err := rebuildSchema(ctx, owner); err != nil {
				migrateErr = err
				return
			}
		}
		// The schema is at head and provably empty exactly here — either
		// because it was just rebuilt, or because reusableClone proved both —
		// which is the only moment a per-table "this is what zero rows costs"
		// baseline can be taken.
		sizes, err := tableSizes(ctx, owner)
		if err != nil {
			migrateErr = fmt.Errorf("recording empty-schema sizes: %w", err)
			return
		}
		emptySizes.Store(&sizes)
		// Last, and only on the success path: Pool refuses to hand out a
		// connection until this is set, so a pool can never predate the schema
		// drop in rebuildSchema.
		schemaReady.Store(true)
	})
	return migrateErr
}

// rebuildSchema drops public and re-applies every embedded core and custom
// migration — the unconditional path, and the one every caller that cannot
// prove its database is a fresh clone still takes.
func rebuildSchema(ctx context.Context, owner *pgx.Conn) error {
	if err := dropPublicSchema(ctx, owner); err != nil {
		return err
	}
	core, err := migrations.Core()
	if err != nil {
		return err
	}
	custom, err := migrations.Custom()
	if err != nil {
		return err
	}
	_, err = dbmigrate.Up(ctx, owner, core, custom)
	return err
}
