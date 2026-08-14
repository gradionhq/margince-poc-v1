// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package collections

import (
	"encoding/json"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
)

// Column references shared across the per-entity segment engines below —
// one spelling each so the archived filter and owner scope stay identical
// across person/organization/deal/lead.
const (
	whereArchivedNull = "t.archived_at IS NULL"
	colOwnerID        = "t.owner_id"
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
// projectEntity is this file's spelling of the project record type, named
// once so the engine key, the table and the column prefix cannot drift.
const projectEntity = "project"

// The record types taggable.entity_type admits (LVS-DDL-2), named through the
// contract's own enum rather than as strings where the enum has a member: a
// renamed member fails to compile here instead of silently dropping a tag
// filter. The tag vocabulary below is built from this set and the fitness
// test reads the same one, so a type dropped here loses its filter visibly.
// project is spelled through projectEntity rather than a contract constant:
// the generated TaggableEntityType enum has no project member even though
// the DDL's CHECK and the spec both admit it (a known, separately tracked
// contract/spec divergence) — projectEntity is this file's own constant for
// the same value, not a stand-in for a missing generated one.
//
// This list is hand-maintained: it catches a member named here without a
// matching vocabulary entry (the loop test below), but it cannot by itself
// notice a type that becomes taggable in the schema and is never added here.
func taggableEntityTypes() []string {
	return []string{
		string(crmcontracts.TaggableEntityTypePerson),
		string(crmcontracts.TaggableEntityTypeOrganization),
		string(crmcontracts.TaggableEntityTypeDeal),
		string(crmcontracts.TaggableEntityTypeLead),
		projectEntity,
	}
}

// tagLinkFor builds the tag field for one entity type: an id reference whose
// column lives in the taggable join, so it compiles as a correlated EXISTS
// (storekit.Field.Link) rather than against the base table. The entity_type is
// baked in per resource because that is what makes the polymorphic join answer
// for THIS record type and no other. taggable carries no workspace_id (dropped
// by 0228); the surrounding transaction is what binds the tenant.
func tagLinkFor(entity string) storekit.Field {
	return storekit.Field{
		Expr: "tg.tag_id",
		Type: storekit.FieldID,
		Link: "EXISTS (SELECT 1 FROM taggable tg WHERE tg.entity_type = '" + entity +
			"' AND tg.entity_id = t.id AND %s)",
	}
}

var segmentEngines = map[string]storekit.Query{
	"person": {
		Table:     "person",
		BaseWhere: whereArchivedNull,
		Fields: map[string]storekit.Field{
			"owner_id": {Expr: colOwnerID, Type: storekit.FieldID},
			"tag":      tagLinkFor("person"),
		},
	},
	"organization": {
		Table: "organization",
		// The installation's own company is never a segment member: a segment
		// answers "which of our accounts match this", and the company running
		// the CRM is not one of them (ADR-0082/A127). In the base clause rather
		// than as a filterable leaf, so no segment can opt back into it and no
		// export built on one can carry it.
		BaseWhere: whereArchivedNull + " AND NOT t.is_anchor",
		Fields: map[string]storekit.Field{
			"owner_id":  {Expr: colOwnerID, Type: storekit.FieldID},
			"industry":  {Expr: "t.industry", Type: storekit.FieldText},
			"size_band": {Expr: "t.size_band", Type: storekit.FieldPicklist},
			"lifecycle": {Expr: "t.lifecycle", Type: storekit.FieldPicklist},
			// RETIRED with the column (ADR-0079/A124), and kept here for the one
			// release it survives: a saved segment written against it must keep
			// evaluating until its author has moved it to lifecycle. Dropping the
			// field would turn every such list into an error at read time, which
			// is a worse answer than a stale one.
			"classification": {Expr: "t.classification", Type: storekit.FieldPicklist},
			"tag":            tagLinkFor("organization"),
		},
	},
	"deal": {
		Table:     "deal",
		BaseWhere: whereArchivedNull,
		Fields: map[string]storekit.Field{
			"pipeline_id":       {Expr: "t.pipeline_id", Type: storekit.FieldID},
			"stage_id":          {Expr: "t.stage_id", Type: storekit.FieldID},
			"owner_id":          {Expr: colOwnerID, Type: storekit.FieldID},
			"organization_id":   {Expr: "t.organization_id", Type: storekit.FieldID},
			"partner_org_id":    {Expr: "t.partner_org_id", Type: storekit.FieldID},
			"project_id":        {Expr: "t.project_id", Type: storekit.FieldID},
			"status":            {Expr: "t.status", Type: storekit.FieldPicklist},
			"forecast_category": {Expr: "t.forecast_category", Type: storekit.FieldPicklist},
			"tag":               tagLinkFor("deal"),
		},
	},
	"lead": {
		Table:     "lead",
		BaseWhere: whereArchivedNull,
		Fields: map[string]storekit.Field{
			"status":            {Expr: "t.status", Type: storekit.FieldPicklist},
			"owner_id":          {Expr: colOwnerID, Type: storekit.FieldID},
			"candidate_org_key": {Expr: "t.candidate_org_key", Type: storekit.FieldText},
			"tag":               tagLinkFor("lead"),
		},
	},
	projectEntity: {
		Table:     projectEntity,
		BaseWhere: whereArchivedNull,
		Fields: map[string]storekit.Field{
			"owner_id":        {Expr: colOwnerID, Type: storekit.FieldID},
			"organization_id": {Expr: "t.organization_id", Type: storekit.FieldID},
			"phase":           {Expr: "t.phase", Type: storekit.FieldPicklist},
			"tag":             tagLinkFor(projectEntity),
		},
	},
}

// SegmentEngine returns the ONE predicate engine (the closed field
// vocabulary, the fixed base clause, and the scope-forcing executor) for a
// filterable resource. Dynamic-list membership (B-E15.11) and filtered
// export (B-E15.13) both draw the engine from here, so the §13.5 filter
// allow-list and the row-scope composition have exactly one spelling. ok
// is false for a resource with no segment engine (activities/partners are
// not predicate-leaf resources — see the vocabulary note above).
func SegmentEngine(resource string) (storekit.Query, bool) {
	q, ok := segmentEngines[resource]
	return q, ok
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
