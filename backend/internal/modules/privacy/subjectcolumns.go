// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package privacy

// The GDPR engines' view of the custom-field catalog. A workspace's
// cf_ columns hold subject data exactly like core columns, so Art. 17
// erasure and the Art. 15 export must cover them — and with ANY catalog
// status, not the record surface's active-only slice: retiring a field
// preserves the physical column and every value in it (the lifecycle
// never DROPs), so a retired column still holds PII the workspace is
// accountable for. The catalog read is workspace-scoped by RLS on
// custom_field; every column name is catalog-derived (server-minted at
// field creation), never client text, and is still identifier-quoted
// before splicing — the same posture as storekit's customcolumns
// mechanics.

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/ports/fieldcatalog"
)

// subjectCustomColumns returns every custom-field column defined on one
// core object in the workspace bound to tx, retired included (see the
// package note above), ordered by column name for deterministic SQL.
func subjectCustomColumns(ctx context.Context, tx pgx.Tx, object string) ([]fieldcatalog.Column, error) {
	rows, err := tx.Query(ctx,
		`SELECT column_name, type FROM custom_field WHERE object = $1 ORDER BY column_name`, object)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[fieldcatalog.Column])
}

// nullColumnAssignments renders the `, "cf_x" = NULL, …` fragment an
// anonymizing UPDATE splices after its fixed SET list — empty when the
// object has no custom columns, so the caller splices unconditionally.
func nullColumnAssignments(cols []fieldcatalog.Column) string {
	var b strings.Builder
	for _, c := range cols {
		b.WriteString(", ")
		b.WriteString(pgx.Identifier{c.Name}.Sanitize())
		b.WriteString(" = NULL")
	}
	return b.String()
}
