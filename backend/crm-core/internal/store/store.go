// Package store is crm-core's persistence layer. Every mutation follows
// the one non-negotiable write shape (data-model §11, events.md §4.2):
// domain row + audit_log row + event_outbox row commit in ONE
// transaction — spelled once in platform storekit (Audit + Emit), called
// on every write path here. There is no write path that skips audit or
// events.
package store

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
)

// Store owns crm-core's tables (data-seam ownership, ADR-0014 Am.1).
type Store struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

func (s *Store) tx(ctx context.Context, fn func(pgx.Tx) error) error {
	return database.WithWorkspaceTx(ctx, s.pool, fn)
}
