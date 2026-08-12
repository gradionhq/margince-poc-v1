// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The custom-field half of this store's reads and writes: the
// workspace's active cf_* columns (fieldcatalog seam) ride the same
// INSERT/UPDATE/SELECT as core columns. All SQL-fragment/value mechanics
// live in storekit's customcolumns helpers (InsertFragments,
// SetCustomFieldPatch, SelectSuffix, ScanDests/ExtractValues) — this
// file keeps only the catalog read the store's operations start from.

import (
	"context"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/fieldcatalog"
)

// activeColumns answers the workspace's active custom columns for one
// object. It runs its own catalog transaction, so callers fetch BEFORE
// opening their write/read transaction (never inside it — a nested pool
// acquire under load is a deadlock shape). A store without a wired
// catalog answers empty: core columns only.
func (s *Store) activeColumns(ctx context.Context, object string) ([]fieldcatalog.Column, error) {
	if s.catalog == nil {
		return nil, nil
	}
	return s.catalog.ActiveColumns(ctx, object)
}

// CustomColumns is the catalog's answer, carried from a caller that had to
// fetch it before it opened its transaction to the seam that runs inside that
// transaction.
//
// The columns are unexported deliberately. They become quoted identifiers in a
// SELECT list and in an UPDATE's SET clause (storekit's customcolumns
// helpers), so a caller able to name its own would be able to widen a read to
// any column of the same table, or to write a core column past the typed input
// this store validates — `fx_rate_to_base` reached through the custom-field
// patch would bypass every money invariant beside it. Only this package can
// populate one, so that is unrepresentable rather than forbidden by comment.
// The zero value is the honest empty answer: core columns only.
type CustomColumns struct {
	cols []fieldcatalog.Column
}

// ActivePersonColumns is the caller-side half of GetPersonTx: a caller that
// opens the transaction itself does this read BEFORE opening it, then threads
// the answer in. That is the same order every store-opened entry point uses;
// it is exported only because the caller of a tx-accepting seam is outside
// this package.
//
// It takes person:read, the grant the read it feeds takes, so the refusal
// still comes before any work — a composite page has no gate of its own above
// this, and the seam's own auth.Require would otherwise run second.
func (s *Store) ActivePersonColumns(ctx context.Context) (CustomColumns, error) {
	return s.activeCustomColumns(ctx, "person", principal.ActionRead)
}

// ActiveOrganizationColumns is ActivePersonColumns for GetOrganizationTx, and
// takes organization:read for the same reason.
func (s *Store) ActiveOrganizationColumns(ctx context.Context) (CustomColumns, error) {
	return s.activeCustomColumns(ctx, "organization", principal.ActionRead)
}

func (s *Store) activeCustomColumns(ctx context.Context, object string, action principal.Action) (CustomColumns, error) {
	if err := auth.Require(ctx, object, action); err != nil {
		return CustomColumns{}, err
	}
	cols, err := s.activeColumns(ctx, object)
	if err != nil {
		return CustomColumns{}, err
	}
	return CustomColumns{cols: cols}, nil
}
