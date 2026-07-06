// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package signals

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// RelationshipStrength is the slice of the §4 explainable strength score
// the warm room consumes: the 0–100 value and its display bucket. The
// full decomposition lives with its owner (the people module); this seam
// carries only what the warm/cold ranking needs.
type RelationshipStrength struct {
	Strength int
	Bucket   string // none | weak | moderate | strong
}

// StrengthSource is the cross-module seam to the §4 relationship-strength
// computation (B-E13.16). The people module implements it; the
// composition layer injects it — signals never imports a sibling.
type StrengthSource interface {
	PersonStrength(ctx context.Context, personID ids.UUID, now time.Time) (RelationshipStrength, error)
}

// Store owns this module's tables (data-seam ownership, ADR-0014 Am.1);
// every write rides the storekit audit+outbox shape in one transaction.
type Store struct {
	pool     *pgxpool.Pool
	strength StrengthSource
}

func NewStore(pool *pgxpool.Pool, strength StrengthSource) *Store {
	return &Store{pool: pool, strength: strength}
}

func (s *Store) tx(ctx context.Context, fn func(pgx.Tx) error) error {
	return database.WithWorkspaceTx(ctx, s.pool, fn)
}

// RequiredFieldError maps to 422 on both surfaces.
type RequiredFieldError struct{ Field string }

func (e *RequiredFieldError) Error() string { return e.Field + " is required" }

// NotResolvableError answers 422: the signal carries nothing the resolver
// could work from, or its resolution is already terminal.
type NotResolvableError struct{ Reason string }

func (e *NotResolvableError) Error() string { return e.Reason }

// NoWarmthError answers 422: warmth/intro-path questions only make sense
// for a signal resolved to an organization (and, for the path, a warm one).
type NoWarmthError struct{ Reason string }

func (e *NoWarmthError) Error() string { return e.Reason }
