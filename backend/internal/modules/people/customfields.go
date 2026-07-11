// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The custom-field half of this store's reads and writes: the
// workspace's active cf_* columns (fieldcatalog seam) ride the same
// INSERT/UPDATE/SELECT as core columns. Value conversion lives in
// storekit's customcolumns helpers; this file only adapts their slices
// to this module's literal SQL and the storekit.Patch write shape.
// Every conversion is drop-on-mismatch: additionalProperties carries no
// per-key contract, so an unknown/retired key or a wrong-shaped value
// is excluded from the write, never a 422.

import (
	"context"
	"strings"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
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

// customInsertFragments adapts storekit.InsertColumns' slices to the
// comma-prefixed fragments a literal INSERT statement splices in after
// its fixed columns/placeholders (empty strings when nothing matches).
func customInsertFragments(active []fieldcatalog.Column, values map[string]any, nextParam int) (cols, holders string, args []any) {
	names, placeholders, bindArgs := storekit.InsertColumns(active, values, nextParam)
	if len(names) == 0 {
		return "", "", nil
	}
	return ", " + strings.Join(names, ", "), ", " + strings.Join(placeholders, ", "), bindArgs
}

// setCustomFieldPatch folds the update's custom-field values into the
// patch — the same Patch that carries core columns, so the audit
// before/after and the version-guarded UPDATE include cf_ changes with
// no extra bookkeeping. current is the row's present wire values (the
// AdditionalProperties map of the pre-update read); an absent key
// diffs from nil.
func setCustomFieldPatch(p *storekit.Patch, active []fieldcatalog.Column, updates, current map[string]any) {
	for _, c := range active {
		v, present := updates[c.Name]
		if !present {
			continue
		}
		sv, ok := storekit.SQLValue(c, v)
		if !ok {
			continue
		}
		p.Set(c.Name, current[c.Name], sv)
	}
}
