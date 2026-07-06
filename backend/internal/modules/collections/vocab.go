// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package collections

import (
	"encoding/json"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
)

// A dynamic (smart) list is a stored filter that the members endpoint
// evaluates live through the ONE predicate engine (B-E15.10/.11). The
// filter names fields from the closed per-resource vocabulary
// (data-model §13.5) — the columnar subset, since a predicate leaf maps
// one field to one indexed column on the base table (the join-backed and
// full-text list params — organization_id-via-employment, q,
// entity_type+entity_id — are list-query surface, not predicate leaves,
// and are deliberately out of the segment vocabulary). Only the four
// list entity types carry a segment engine; list.entity_type constrains
// membership to exactly these tables.
var segmentEngines = map[string]storekit.Query{
	"person": {
		Table:     "person",
		BaseWhere: "t.archived_at IS NULL",
		Fields: map[string]storekit.Field{
			"owner_id": {Expr: "t.owner_id", Type: storekit.FieldID},
		},
	},
	"organization": {
		Table:     "organization",
		BaseWhere: "t.archived_at IS NULL",
		Fields: map[string]storekit.Field{
			"owner_id":       {Expr: "t.owner_id", Type: storekit.FieldID},
			"industry":       {Expr: "t.industry", Type: storekit.FieldText},
			"size_band":      {Expr: "t.size_band", Type: storekit.FieldPicklist},
			"classification": {Expr: "t.classification", Type: storekit.FieldPicklist},
		},
	},
	"deal": {
		Table:     "deal",
		BaseWhere: "t.archived_at IS NULL",
		Fields: map[string]storekit.Field{
			"pipeline_id":       {Expr: "t.pipeline_id", Type: storekit.FieldID},
			"stage_id":          {Expr: "t.stage_id", Type: storekit.FieldID},
			"owner_id":          {Expr: "t.owner_id", Type: storekit.FieldID},
			"organization_id":   {Expr: "t.organization_id", Type: storekit.FieldID},
			"partner_org_id":    {Expr: "t.partner_org_id", Type: storekit.FieldID},
			"status":            {Expr: "t.status", Type: storekit.FieldPicklist},
			"forecast_category": {Expr: "t.forecast_category", Type: storekit.FieldPicklist},
		},
	},
	"lead": {
		Table:     "lead",
		BaseWhere: "t.archived_at IS NULL",
		Fields: map[string]storekit.Field{
			"status":            {Expr: "t.status", Type: storekit.FieldPicklist},
			"owner_id":          {Expr: "t.owner_id", Type: storekit.FieldID},
			"candidate_org_key": {Expr: "t.candidate_org_key", Type: storekit.FieldText},
		},
	},
}

// predicateFromDefinition decodes a dynamic list's stored `definition`
// jsonb into the canonical predicate tree. The definition IS the filter
// tree (and/or/field/op/value) — no wrapper — so the round-trip is a
// direct re-marshal into storekit.Predicate.
func predicateFromDefinition(def map[string]any) (storekit.Predicate, error) {
	raw, err := json.Marshal(def)
	if err != nil {
		return storekit.Predicate{}, err
	}
	var p storekit.Predicate
	if err := json.Unmarshal(raw, &p); err != nil {
		return storekit.Predicate{}, &BadInputError{Field: "definition", Reason: "is not a valid filter tree"}
	}
	return p, nil
}
