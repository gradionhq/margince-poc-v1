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
