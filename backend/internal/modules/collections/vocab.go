// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package collections

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/ports/fieldcatalog"
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

// catalogObjectFor maps a segment resource onto the custom_field.object it
// carries columns under. The two vocabularies are not identical: an activity has
// custom fields and no segment engine, a project has an engine and no custom
// fields, so an absent entry means "this resource has no custom columns" rather
// than an error.
func catalogObjectFor(resource string) (string, bool) {
	switch resource {
	case "person", "organization", "deal", "lead":
		return resource, true
	default:
		return "", false
	}
}

// SegmentEngine returns the ONE predicate engine for a filterable resource: the
// closed core vocabulary, widened with this workspace's active and retired cf_*
// columns, plus the fixed base clause and the scope-forcing executor. Dynamic-list
// validation, membership evaluation and filtered export all resolve it HERE — the
// export handler through this same exported method, not a package-level lookup of
// its own — so the vocabulary cannot differ between what a filter is allowed to
// say, what it selects, and what an export of it contains (LVS-AC-2, one engine).
//
// ok is false for a resource with no engine at all (activities and partners are
// not predicate-leaf resources); the caller decides what that means.
func (s *Store) SegmentEngine(ctx context.Context, resource string) (storekit.Query, bool, error) {
	core, ok := segmentEngines[resource]
	if !ok {
		return storekit.Query{}, false, nil
	}
	// A COPY, always: segmentEngines is process-wide and its Fields map is
	// shared, so merging in place would leak one workspace's custom vocabulary
	// into every later request.
	merged := core
	merged.Fields = make(map[string]storekit.Field, len(core.Fields))
	for name, field := range core.Fields {
		merged.Fields[name] = field
	}
	object, hasCustom := catalogObjectFor(resource)
	if s.catalog == nil || !hasCustom {
		return merged, true, nil
	}
	columns, err := s.catalog.FilterableColumns(ctx, object)
	if err != nil {
		return storekit.Query{}, false, fmt.Errorf("read the custom-field vocabulary for %s: %w", resource, err)
	}
	for _, column := range columns {
		// The core vocabulary wins a name collision: `cf_` prefixing is a Go-side
		// convention (customfields' engine, not a DDL CHECK), so a catalogue row
		// named after a core column is a possibility the merge has to defend
		// against, not one it can assume away. Letting the catalogue win would
		// silently retype a core field (e.g. a uuid owner_id reading as free
		// text) rather than fail loudly, which is a worse outcome than the
		// colliding custom column simply never reaching the filter vocabulary.
		if _, coreOwns := core.Fields[column.Name]; coreOwns {
			continue
		}
		field, err := customField(column)
		if err != nil {
			return storekit.Query{}, false, err
		}
		merged.Fields[column.Name] = field
	}
	return merged, true, nil
}

// customField types one custom column for the predicate engine. The six catalog
// types and the six filter types are the same closed set spelled in two packages,
// so the mapping is total — and an unrecognised value fails rather than defaulting
// to text, which would admit `contains` on a number and read as a working filter.
func customField(column fieldcatalog.Column) (storekit.Field, error) {
	var fieldType storekit.FieldType
	switch column.Type {
	case fieldcatalog.TypeText:
		fieldType = storekit.FieldText
	case fieldcatalog.TypeNumber:
		fieldType = storekit.FieldNumber
	case fieldcatalog.TypeDate:
		fieldType = storekit.FieldDate
	case fieldcatalog.TypeCurrency:
		fieldType = storekit.FieldCurrency
	case fieldcatalog.TypePicklist:
		fieldType = storekit.FieldPicklist
	case fieldcatalog.TypeBoolean:
		fieldType = storekit.FieldBoolean
	default:
		return storekit.Field{}, fmt.Errorf(
			"custom column %s carries type %q, which the filter engine has no operators for",
			column.Name, column.Type)
	}
	return storekit.Field{Expr: `t.` + pgx.Identifier{column.Name}.Sanitize(), Type: fieldType}, nil
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
