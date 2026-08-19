// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package collections

import (
	"context"
	"encoding/json"
	"errors"
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
// and are deliberately out of the segment vocabulary). Every list entity
// type carries a segment engine; list.entity_type constrains membership to
// exactly these tables, and the map below is the authority the export
// object's own vocabulary answers to.
// projectEntity is this file's spelling of the project record type, named
// once so the engine key, the table and the column prefix cannot drift.
const projectEntity = "project"

// tagFilterField is the one filter-vocabulary key every taggable entity's
// segment engine exposes for its tag leaf — named once so the five
// per-entity Fields maps below cannot drift onto a different spelling.
const tagFilterField = "tag"

// ownerIDField is the filter-vocabulary key every segment engine below
// exposes for colOwnerID — the vocabulary's name for the field, as
// distinct from colOwnerID itself (the SQL expression it compiles to).
const ownerIDField = "owner_id"

// The record types taggable.entity_type admits (LVS-DDL-2), named through the
// contract's own enum rather than as strings: a renamed member fails to compile
// here instead of silently dropping a tag filter. The tag vocabulary below is
// built from this set and the fitness test reads the same one, so a type
// dropped here loses its filter visibly.
//
// This list is hand-maintained: it catches a member named here without a
// matching vocabulary entry (the loop test below), but it cannot by itself
// notice a type that becomes taggable in the schema and is never added here.
// The integration lane closes that side by comparing the DDL's own CHECK.
func taggableEntityTypes() []string {
	return []string{
		string(crmcontracts.TaggableEntityTypePerson),
		string(crmcontracts.TaggableEntityTypeOrganization),
		string(crmcontracts.TaggableEntityTypeDeal),
		string(crmcontracts.TaggableEntityTypeLead),
		string(crmcontracts.TaggableEntityTypeProject),
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

// customerLink is the EXISTS template a deal's filter reaches its customer
// through: one correlated subquery per leaf, on the organization the deal
// already points at.
//
// It does NOT re-apply the organization engine's own base clause (archived and
// is_anchor), and that is the substantive choice here. Those two exclusions
// answer "which of our accounts are segment MEMBERS"; this leaf answers a fact
// about the company a deal belongs to, which archiving does not change. Carrying
// them over would move deals out of "the manufacturing pipeline" the moment
// somebody archived a company — a pipeline figure shifting for a reason nobody
// filtering could see.
//
// The organization table is read inside the caller's own transaction, so the RLS
// GUC contract binds it exactly as it binds the base table: there is no path
// here that reaches another workspace's rows.
const customerLink = "EXISTS (SELECT 1 FROM organization o WHERE o.id = t.organization_id AND %s)"

// customerField types one organization column as a deal-side filter leaf. The
// operators it advertises narrow themselves — OperatorsFor reads Link — so an
// industry reached this way offers everything text does except `contains`.
func customerField(column string, fieldType storekit.FieldType) storekit.Field {
	return storekit.Field{Expr: "o." + column, Type: fieldType, Link: customerLink}
}

var segmentEngines = map[string]storekit.Query{
	"person": {
		Table:     "person",
		BaseWhere: whereArchivedNull,
		Fields: map[string]storekit.Field{
			ownerIDField:   {Expr: colOwnerID, Type: storekit.FieldID},
			tagFilterField: tagLinkFor("person"),
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
			ownerIDField: {Expr: colOwnerID, Type: storekit.FieldID},
			"industry":   {Expr: "t.industry", Type: storekit.FieldText},
			"size_band":  {Expr: "t.size_band", Type: storekit.FieldPicklist},
			"lifecycle":  {Expr: "t.lifecycle", Type: storekit.FieldPicklist},
			// RETIRED with the column (ADR-0079/A124), and kept here for the one
			// release it survives: a saved segment written against it must keep
			// evaluating until its author has moved it to lifecycle. Dropping the
			// field would turn every such list into an error at read time, which
			// is a worse answer than a stale one. Named in retiredCoreFields
			// below, so no surface OFFERS it for a new clause.
			"classification": {Expr: "t.classification", Type: storekit.FieldPicklist},
			tagFilterField:   tagLinkFor("organization"),
		},
	},
	"deal": {
		Table:     "deal",
		BaseWhere: whereArchivedNull,
		Fields: map[string]storekit.Field{
			"pipeline_id":       {Expr: "t.pipeline_id", Type: storekit.FieldID},
			"stage_id":          {Expr: "t.stage_id", Type: storekit.FieldID},
			ownerIDField:        {Expr: colOwnerID, Type: storekit.FieldID},
			"organization_id":   {Expr: "t.organization_id", Type: storekit.FieldID},
			"partner_org_id":    {Expr: "t.partner_org_id", Type: storekit.FieldID},
			"project_id":        {Expr: "t.project_id", Type: storekit.FieldID},
			"status":            {Expr: "t.status", Type: storekit.FieldPicklist},
			"forecast_category": {Expr: "t.forecast_category", Type: storekit.FieldPicklist},
			tagFilterField:      tagLinkFor("deal"),
			// The customer's own attributes, so "the pipeline for manufacturing"
			// is a filter rather than a spreadsheet. Same columns and same types
			// as the organization engine offers directly, reached through the
			// deal's organization_id.
			//
			// classification is deliberately absent. It is retired (ADR-0079/A124)
			// and survives on the organization engine only so segments already
			// written against it keep evaluating — a NEW way to name it would be
			// a fresh dependency on a column that is going away.
			"organization_industry":  customerField("industry", storekit.FieldText),
			"organization_size_band": customerField("size_band", storekit.FieldPicklist),
			"organization_lifecycle": customerField("lifecycle", storekit.FieldPicklist),
		},
	},
	"lead": {
		Table:     "lead",
		BaseWhere: whereArchivedNull,
		Fields: map[string]storekit.Field{
			"status":            {Expr: "t.status", Type: storekit.FieldPicklist},
			ownerIDField:        {Expr: colOwnerID, Type: storekit.FieldID},
			"candidate_org_key": {Expr: "t.candidate_org_key", Type: storekit.FieldText},
			tagFilterField:      tagLinkFor("lead"),
		},
	},
	projectEntity: {
		Table:     projectEntity,
		BaseWhere: whereArchivedNull,
		Fields: map[string]storekit.Field{
			ownerIDField:      {Expr: colOwnerID, Type: storekit.FieldID},
			"organization_id": {Expr: "t.organization_id", Type: storekit.FieldID},
			"phase":           {Expr: "t.phase", Type: storekit.FieldPicklist},
			tagFilterField:    tagLinkFor(projectEntity),
		},
	},
}

// retiredCoreFields names core vocabulary entries that a filter may still SAY
// and no surface may still OFFER, per resource.
//
// Retirement has two sources and they are genuinely different questions, so this
// is deliberately not one mechanism with the custom-field half. A custom column's
// status is per-workspace admin state, read from the catalogue; a core field's is
// a decision in this file, taken by an ADR, identical in every installation. A
// map keyed by a name the catalogue has never heard of is the only place the
// second can live — organization.classification has no `custom_field` row, so no
// catalogue read and no client-side join can ever discover that it is retired.
//
// Keyed by resource rather than by bare name: two resources may legitimately
// carry a field of the same name where only one of them has retired it.
var retiredCoreFields = map[string]map[string]bool{
	// ADR-0079/A124 replaced it with lifecycle.
	"organization": {"classification": true},
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
	if s.catalog == nil {
		return merged, true, nil
	}
	// Every resource that reaches this point owns a segment engine, and
	// customfields.FieldObjects admits exactly that same set — person,
	// organization, deal, lead, project — so resource IS the catalog's
	// object key; no separate mapping to maintain or drift out of sync.
	columns, err := s.catalog.FilterableColumns(ctx, resource)
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
		field, ok := customField(column)
		if !ok {
			continue
		}
		merged.Fields[column.Name] = field
	}
	return merged, true, nil
}

// customFieldTypes maps the six closed catalog types onto the predicate
// engine's own. Both sets are closed and neither is this file's to extend: a
// seventh catalog type arrives with its own entry here, and
// TestEveryCustomFieldTypeIsFilterable fails until it has one.
var customFieldTypes = map[string]storekit.FieldType{
	fieldcatalog.TypeText:     storekit.FieldText,
	fieldcatalog.TypeNumber:   storekit.FieldNumber,
	fieldcatalog.TypeDate:     storekit.FieldDate,
	fieldcatalog.TypeCurrency: storekit.FieldCurrency,
	fieldcatalog.TypePicklist: storekit.FieldPicklist,
	fieldcatalog.TypeBoolean:  storekit.FieldBoolean,
}

// customField types one custom column for the predicate engine, and answers
// false for a catalog type this engine has no operators for.
//
// LEFT OUT rather than refused, and the difference is the blast radius. This
// mapping runs over EVERY column of the object, so a refusal here costs the
// whole resolution — list-create validation, membership evaluation and
// filtered export all fail for that record type, including for filters that
// never name the field. Omitting contains the damage to the one field, which
// is the same call `search`'s vocabulary makes next door on its own stated
// grounds — an unasked field is a smaller failure than one that answers the
// wrong comparison.
//
// Omission does not hide anything either, because a field the vocabulary does
// not carry is not silently dropped from a predicate: CompilePredicate refuses
// an unknown name with CodeFilterFieldNotAllowed, so a saved segment that
// actually NAMES such a field says "not filterable on this resource" rather
// than quietly matching a different set of rows. What omission must never
// become is a guess — defaulting an unknown type to text would admit
// `contains` on a number and read as a working filter — which is why the
// mapping is a closed map with a gate over it rather than a switch with a
// fallback.
func customField(column fieldcatalog.Column) (storekit.Field, bool) {
	fieldType, ok := customFieldTypes[column.Type]
	if !ok {
		return storekit.Field{}, false
	}
	return storekit.Field{Expr: `t.` + pgx.Identifier{column.Name}.Sanitize(), Type: fieldType}, true
}

// errNotAFilterTree is what a jsonb value that does not decode into the
// canonical predicate comes back as. It deliberately carries no wire field:
// which field to name — or whether to name one at all — is the caller's
// question, not this decoder's. The same tree arrives under `definition` from
// a dynamic list, inside `query` from a saved view, and from neither on an
// export or a membership read, where the caller sent only an id and a field
// error would tell them to fix something they never wrote.
var errNotAFilterTree = errors.New("not a valid filter tree")

// predicateFromDefinition decodes a stored filter tree jsonb into the
// canonical predicate. The stored value IS the tree (and/or/field/op/value) —
// no wrapper — so the round-trip is a direct re-marshal into
// storekit.Predicate.
func predicateFromDefinition(def map[string]any) (storekit.Predicate, error) {
	raw, err := json.Marshal(def)
	if err != nil {
		return storekit.Predicate{}, err
	}
	var p storekit.Predicate
	if err := json.Unmarshal(raw, &p); err != nil {
		return storekit.Predicate{}, errNotAFilterTree
	}
	return p, nil
}

// The one caller that dresses this as a field fault is compileForValidation,
// where the caller genuinely sent the tree; every read path wraps it as the
// invariant break it is instead.
