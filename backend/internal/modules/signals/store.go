// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package signals

import (
	"context"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

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
	PersonStrength(ctx context.Context, personID ids.PersonID, now time.Time) (RelationshipStrength, error)
}

// Store owns this module's tables (data-seam ownership, ADR-0014 Am.1);
// every write rides the storekit audit+outbox shape in one transaction.
type Store struct {
	// db binds the workspace this store runs for (ADR-0091 §9 step 3).
	db       *database.DB
	strength StrengthSource
}

// NewStore opens this module's store on a handle already bound to the
// workspace it serves.
func NewStore(db *database.DB, strength StrengthSource) *Store {
	return &Store{db: db, strength: strength}
}

func (s *Store) tx(ctx context.Context, fn func(pgx.Tx) error) error {
	return s.db.Tx(ctx, fn)
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

// signalEntityTables is the store-side spelling of the schema's
// signal_entity_type CHECK: a signal's subject is a deal, organization,
// person or project. The client-supplied type flows on to a table-name seam
// (the link-target probe), so the store pins the set itself instead of
// leaning on transport enum validation alone.
// TestSignalEntityTablesMatchTheSchemaCheck holds it to the constraint.
var signalEntityTables = map[string]bool{"deal": true, "organization": true, "person": true, "project": true}

// SignalEntityTables lists the subject types a signal may carry, sorted —
// the same set the schema CHECK admits, for a reader that has to spell it.
func SignalEntityTables() []string {
	return slices.Sorted(maps.Keys(signalEntityTables))
}

// InvalidSignalEntityTypeError answers 422: the subject type is outside
// the signal_entity_type set.
type InvalidSignalEntityTypeError struct{ EntityType string }

func (e *InvalidSignalEntityTypeError) Error() string {
	return "entity_type " + e.EntityType + " is not one of " + strings.Join(SignalEntityTables(), ", ")
}
