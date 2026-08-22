// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// A report dimension over a REFERENCE column hands its ids back in the
// aggregate. The engine's own gate covers the report's entity and says nothing
// about the records those rows point at, while a normal read of the same row
// masks exactly those references when the caller cannot open them
// (deals/fieldmask.go). So every reference dimension has to declare the table
// it points at, and this derives that obligation from the catalog rather than
// keeping a second list of which ones were remembered.

import (
	"strings"
	"testing"
)

// referenceColumns are the row-scoped records a report's rows can point AT,
// mapped to the table each names. A column here that a spec exposes as a
// dimension without declaring its scope is the finding.
//
// gatekit:fixture the reference columns a report row can carry, and the table each points at
var referenceColumns = map[string]string{
	colOrganizationID:     tableOrganization,
	colPartnerOrgID:       tableOrganization,
	colProjectID:          tableProject,
	activityProjectIDExpr: tableProject,
}

func TestEveryReferenceDimensionDeclaresItsScope(t *testing.T) {
	for name, spec := range prebuiltReports {
		for field, expr := range spec.dimensions {
			table, isReference := referenceColumns[expr]
			if !isReference {
				continue
			}
			declared, ok := spec.referenceScopes[expr]
			if !ok {
				t.Errorf("%s groups by %q, which names a row in %s the caller may not be able to open, "+
					"but declares no referenceScopes entry — the aggregate would report an id "+
					"this caller's own read of the same row masks",
					name, field, table)
				continue
			}
			if declared != table {
				t.Errorf("%s scopes %q against %q, want %q", name, field, declared, table)
			}
		}
	}
}

// The reverse direction: a declared scope must name a table the row-scope
// helpers actually know, or the clause it renders would fail at query time on
// a path no unit test reaches.
func TestEveryDeclaredReferenceScopeNamesARowScopedTable(t *testing.T) {
	for name, spec := range prebuiltReports {
		for column, table := range spec.referenceScopes {
			// A reference is a column of the row (`t.x`) or a scalar read off
			// the row (`... WHERE al.activity_id = t.id`); either way it must
			// name the report's own row alias, or the clause binds nothing.
			if !strings.Contains(column, "t.") {
				t.Errorf("%s scopes %q, which does not read the report's own row", name, column)
			}
			if table == "" {
				t.Errorf("%s scopes %q against no table", name, column)
			}
		}
	}
}
