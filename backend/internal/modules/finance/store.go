// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package finance

// The store: reads of the mirror, and nothing else.
//
// There is no create, update or delete on a finance RECORD anywhere in this
// file, and that is the read-only posture (FIN-DDL-N-1) expressed where a
// contributor would look for the write rather than as a runtime refusal. Rows
// arrive through the sync pass, which is the connector's own path.

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/settings"
)

// Store reads the finance mirror under the caller's own gates.
type Store struct {
	pool *pgxpool.Pool
	// now is injected so a summary's staleness and its trailing window are
	// testable without waiting for the clock to move.
	now func() time.Time
	// settings resolves the installation's base currency (ADR-0090/A135),
	// injected the same way the clock is.
	settings *settings.Store
}

// NewStore binds the store to the pool every tenant read runs through.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, now: time.Now}
}

// WithSettings injects the installation-settings seam the mirror reads its
// base currency from (ADR-0090/A135). A store without it refuses rather than
// falling back to the retiring workspace column: two answers to one question
// is how the copies drift apart unnoticed.
func (s *Store) WithSettings(store *settings.Store) *Store {
	s.settings = store
	return s
}

// WithClock replaces the store's clock. Tests only: a summary that reads
// "stale" is a function of the wall clock, and a test that waited for one
// would be a test that sometimes fails.
func (s *Store) WithClock(now func() time.Time) *Store {
	s.now = now
	return s
}

func (s *Store) tx(ctx context.Context, fn func(pgx.Tx) error) error {
	return database.WithWorkspaceTx(ctx, s.pool, fn)
}

// staleAfter is how long a successful sync stays current.
//
// The sweep runs every six hours, so a mirror that has not synced in a full
// day has missed four passes — long enough that the reader should see the date
// beside the figure rather than take it as today's.
const staleAfter = 24 * time.Hour
