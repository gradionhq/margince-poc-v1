// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// SoR-mode schema introspection (interfaces.md §3): the descriptor set
// ListObjects/ListFields serve and the ad-hoc report vocabulary. Static
// by design (P11: declared in code, versioned with it); fork-owned x_
// columns join through the custom seam with Custom=true.

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
}

func schemaFields(entity datasource.EntityType) ([]datasource.FieldDef, bool) {
	for _, obj := range schemaObjects {
		if obj.Type == entity {
			return obj.Fields, true
		}
	}
	return nil, false
}
