// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// SoR-mode schema introspection (interfaces.md §3): the descriptor set
// ListObjects/ListFields serve and the ad-hoc report vocabulary. Static
// by design (P11: declared in code, versioned with it); fork-owned x_
// columns join through the custom seam with Custom=true.
//
// Every datasource.RecordType owes an entry here — a type the surface
// admits to create, tag and list, and then cannot describe or report on,
// is announced and not served. `activity` holds one too, so the set is a
// superset of the record vocabulary rather than a copy of it: a timeline
// event is reportable through the link-walk even though nothing points at
// one.
//
// `relationship` deliberately holds none. An entry here IS the ad-hoc
// report vocabulary, and runAdHocPlan scopes those rows with
// auth.ScopeClauseFor over the entity's own table — which serves only
// tables carrying owner_id. An edge has no owner: its visibility is its
// two endpoints'. A relationship descriptor would therefore advertise a
// groupable object whose plan errors on the scope clause instead of
// refusing, and it earns one when the report path can walk to the anchor
// record's visibility instead.

import (
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

var schemaObjects = []datasource.ObjectDef{
	{Type: datasource.EntityPerson, Label: "Person", Fields: []datasource.FieldDef{
		{Name: "full_name", Type: "text"},
		{Name: "owner_id", Type: "uuid", Nullable: true},
		{Name: "source", Type: "text"},
		{Name: "created_at", Type: "timestamptz"},
	}},
	{Type: datasource.EntityOrganization, Label: "Organization", Fields: []datasource.FieldDef{
		{Name: "display_name", Type: "text"},
		{Name: "legal_name", Type: "text", Nullable: true},
		{Name: "industry", Type: "text", Nullable: true},
		{Name: "owner_id", Type: "uuid", Nullable: true},
		{Name: "created_at", Type: "timestamptz"},
	}},
	{Type: datasource.EntityDeal, Label: "Deal", Fields: []datasource.FieldDef{
		{Name: "name", Type: "text"},
		{Name: "amount_minor", Type: "bigint", Nullable: true},
		{Name: "currency", Type: "char(3)", Nullable: true},
		{Name: "status", Type: "text"},
		{Name: "pipeline_id", Type: "uuid"},
		{Name: "stage_id", Type: "uuid"},
		{Name: "organization_id", Type: "uuid", Nullable: true},
		{Name: "owner_id", Type: "uuid", Nullable: true},
		{Name: "expected_close_date", Type: "date", Nullable: true},
		{Name: "created_at", Type: "timestamptz"},
	}},
	{Type: datasource.EntityLead, Label: "Lead", Fields: []datasource.FieldDef{
		{Name: "full_name", Type: "text", Nullable: true},
		{Name: "company_name", Type: "text", Nullable: true},
		{Name: "email", Type: "text", Nullable: true},
		{Name: "status", Type: "text"},
		{Name: "owner_id", Type: "uuid", Nullable: true},
		{Name: "created_at", Type: "timestamptz"},
	}},
	{Type: datasource.EntityActivity, Label: "Activity", Fields: []datasource.FieldDef{
		{Name: "kind", Type: "text"},
		{Name: "subject", Type: "text", Nullable: true},
		{Name: "direction", Type: "text", Nullable: true},
		{Name: "is_done", Type: "boolean"},
		{Name: "occurred_at", Type: "timestamptz"},
	}},
	{Type: datasource.EntityProject, Label: "Project", Fields: []datasource.FieldDef{
		{Name: "name", Type: "text"},
		{Name: "key", Type: "text", Nullable: true},
		{Name: "organization_id", Type: "uuid"},
		{Name: "owner_id", Type: "uuid", Nullable: true},
		{Name: "phase", Type: "text"},
		{Name: "started_at", Type: "date", Nullable: true},
		{Name: "target_end_date", Type: "date", Nullable: true},
		{Name: "ended_at", Type: "date", Nullable: true},
		{Name: "created_at", Type: "timestamptz"},
	}},
}

func schemaFields(entity datasource.EntityType) ([]datasource.FieldDef, bool) {
	for _, obj := range schemaObjects {
		if obj.Type == entity {
			return obj.Fields, true
		}
	}
	return nil, false
}
