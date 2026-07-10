// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// Store owns this module's tables (data-seam ownership, ADR-0014 Am.1);
// every write rides the storekit audit+outbox shape in one transaction.
type Store struct {
	pool *pgxpool.Pool
	// blob backs the attachment endpoints; nil in a role that stores no
	// objects, in which case the attachment handlers answer 501 rather than
	// nil-deref (WithBlobstore is how a role opts in).
	blob blobstore.Store
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// WithBlobstore returns a store that backs the attachment endpoints with the
// given object store. It returns a copy so the base store stays unchanged.
func (s *Store) WithBlobstore(blob blobstore.Store) *Store {
	clone := *s
	clone.blob = blob
	return &clone
}

func (s *Store) tx(ctx context.Context, fn func(pgx.Tx) error) error {
	return database.WithWorkspaceTx(ctx, s.pool, fn)
}

// sprintf keeps SQL assembly lines readable; arguments are always
// placeholder indexes or clamped ints, never user input.
func sprintf(format string, a ...any) string { return fmt.Sprintf(format, a...) }

func uuidPtr(id *ids.UUID) *openapi_types.UUID {
	if id == nil {
		return nil
	}
	converted := openapi_types.UUID(*id)
	return &converted
}

// workspaceID types the tx-bound workspace GUC (storekit hands it out
// untyped) for the helpers that carry it as an entity parameter.
func workspaceID(ctx context.Context) ids.WorkspaceID {
	return ids.From[ids.WorkspaceKind](storekit.MustWorkspace(ctx))
}
