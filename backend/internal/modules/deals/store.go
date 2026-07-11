// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/fieldcatalog"
)

// Store owns this module's tables (data-seam ownership, ADR-0014 Am.1);
// every write rides the storekit audit+outbox shape in one transaction.
type Store struct {
	pool *pgxpool.Pool
	// catalog is the fieldcatalog seam (custom-field columns); nil means
	// no catalog is wired and every read/write runs core-columns-only.
	catalog fieldcatalog.Reader
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// WithFieldCatalog wires the workspace custom-field catalog in
// (compose injects modules/customfields' Service here — ADR-0054: a
// module never imports a sibling), making active cf_* columns
// participate in deal reads and writes.
func (s *Store) WithFieldCatalog(catalog fieldcatalog.Reader) *Store {
	s.catalog = catalog
	return s
}

// activeColumns answers the workspace's active custom columns for the
// deal object (this store's one record type). It runs its own catalog
// transaction, so callers fetch BEFORE opening their write/read
// transaction (never inside it — a nested pool acquire under load is a
// deadlock shape). A store without a wired catalog answers empty: core
// columns only.
func (s *Store) activeColumns(ctx context.Context) ([]fieldcatalog.Column, error) {
	if s.catalog == nil {
		return nil, nil
	}
	return s.catalog.ActiveColumns(ctx, "deal")
}

func (s *Store) tx(ctx context.Context, fn func(pgx.Tx) error) error {
	return database.WithWorkspaceTx(ctx, s.pool, fn)
}

func uuidPtr(id *ids.UUID) *openapi_types.UUID {
	if id == nil {
		return nil
	}
	converted := openapi_types.UUID(*id)
	return &converted
}
