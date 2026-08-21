// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package privacy

// The GDPR engines' view of the custom-field catalog. A workspace's
// cf_ columns hold subject data exactly like core columns, so Art. 17
// erasure and the Art. 15 export must cover them — and with ANY catalog
// status, not the record surface's active-only slice: retiring a field
// preserves the physical column and every value in it (the lifecycle
// never DROPs), so a retired column still holds PII the workspace is
// accountable for. The catalog read reaches every custom_field in the
// installation — core 0229 dropped the table's workspace column, so there
// is nothing narrower to read, and A107/ADR-0061's one organization per
// installation is what makes that the right set. Every column name is
// catalog-derived (server-minted at field creation), never client text,
// and is still identifier-quoted
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
	defer rows.Close()
	// Scanned into NAMED fields rather than by position. fieldcatalog.Column
	// belongs to the port, not to this module, so a positional mapping bound this
	// two-column SELECT to a struct shape privacy has no say over: the day the
	// port gained a third field for somebody else's read, this query started
	// failing on a mismatch it never mentioned. Only the columns erasure and the
	// Art. 15 export actually use are read.
	var cols []fieldcatalog.Column
	for rows.Next() {
		var c fieldcatalog.Column
		if err := rows.Scan(&c.Name, &c.Type); err != nil {
			return nil, err
		}
		cols = append(cols, c)
	}
	return cols, rows.Err()
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
