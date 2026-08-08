// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package search

// The resolved query vocabulary: what a plan may name, for ONE caller, right
// now (SEARCH-PARAM-7). It is composed rather than stored, from three sources
// that each already answer their part of the question:
//
//   - the generated contract types, for the core fields (queryfields.go);
//   - the live custom-field catalog, through the fieldcatalog seam, for the
//     workspace's own columns;
//   - object RBAC, for which record types this principal may read at all.
//
// Nothing here is a list to maintain, which is the property SEARCH-AC-15
// tests: a custom field added is askable on the next resolve and a retired
// one is gone, with no edit anywhere.
//
// The RBAC filter is what makes the vocabulary PER-CALLER (SEARCH-AC-16). A
// record type the principal cannot read contributes no fields and no
// relations, so naming one of its fields is refused exactly as an invented
// name is — same code, same wording. A vocabulary that refused it differently
// would be a field-discovery channel: probe until the wording changes.

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/fieldcatalog"
)

// FieldKind is the closed set of value shapes a predicate operand can have.
// A field's kind decides its operators, so this set and the operator sets
// below are the whole of what "op" may say.
type FieldKind string

// The seven kinds. Each maps onto one operator set below, and a field's kind
// is derived from its declared type — never chosen.
const (
	KindText      FieldKind = "text"
	KindNumber    FieldKind = "number"
	KindBoolean   FieldKind = "boolean"
	KindDate      FieldKind = "date"
	KindTimestamp FieldKind = "timestamp"
	KindID        FieldKind = "id"
	// KindGeo is a place rather than a value: the operand `within_radius`
	// measures from. It admits no exact operator — a place does not compare
	// equal to anything — and its component values are askable as ordinary
	// text through the nested leaves (`address.city`).
	KindGeo FieldKind = "geo"
)

// The operators a plan may name. Named once so the operator sets, the validator and the
// published document cannot disagree about their spelling.
const (
	OpEq           = "eq"
	OpNeq          = "neq"
	OpIn           = "in"
	OpLt           = "lt"
	OpLte          = "lte"
	OpGt           = "gt"
	OpGte          = "gte"
	OpWithinRadius = "within_radius"
)

// operatorsByKind derives a field's operators from its kind. Ordered types
// add the four comparisons; a boolean admits equality alone (`neq true` is
// `eq false`, and offering both invites a plan that says the same thing two
// ways); a place admits only the radius operator.
var operatorsByKind = map[FieldKind][]string{
	KindText:      {OpEq, OpNeq, OpIn},
	KindID:        {OpEq, OpNeq, OpIn},
	KindBoolean:   {OpEq},
	KindNumber:    {OpEq, OpNeq, OpIn, OpLt, OpLte, OpGt, OpGte},
	KindDate:      {OpEq, OpNeq, OpIn, OpLt, OpLte, OpGt, OpGte},
	KindTimestamp: {OpEq, OpNeq, OpIn, OpLt, OpLte, OpGt, OpGte},
	KindGeo:       {OpWithinRadius},
}

// Field is one member of the resolved vocabulary.
type Field struct {
	Name string
	Kind FieldKind
	// Ops is derived from Kind at construction. It is carried rather than
	// re-derived on every check so the published document and the validator
	// read the same value, not two computations of it.
	Ops []string
}

// newField is the ONE way a Field comes into existence, so a field whose
// operators disagree with its kind is unrepresentable.
//
// Ops is CLONED off the kind's set. A Field travels out of this package — the
// published document and any future executor both read it — and handing out
// the map's own slice would let one caller's edit rewrite what every field of
// that kind admits, for every later caller. Cloning here is the same defence
// contractFields applies to the field list itself.
func newField(name string, kind FieldKind) Field {
	return Field{Name: name, Kind: kind, Ops: slices.Clone(operatorsByKind[kind])}
}

// Relation is one depth-1 hop.
type Relation struct {
	// Name is what a plan says; Target the record type the hop lands on,
	// whose vocabulary the hop's predicates are checked against.
	Name   string
	Target string
	// Via names the contract reference the edge is derived from. It is
	// published so a caller can see WHY a hop exists, and it is what E2
	// executes the join on.
	Via string
}

// TargetVocabulary is everything askable about one record type.
type TargetVocabulary struct {
	Target    string
	Fields    []Field
	Relations []Relation
}

// Field answers the named field, and false when this caller may not name it.
func (v TargetVocabulary) Field(name string) (Field, bool) {
	i := slices.IndexFunc(v.Fields, func(f Field) bool { return f.Name == name })
	if i < 0 {
		return Field{}, false
	}
	return v.Fields[i], true
}

// Relation answers the named hop, and false when this caller may not take it.
func (v TargetVocabulary) Relation(name string) (Relation, bool) {
	i := slices.IndexFunc(v.Relations, func(r Relation) bool { return r.Name == name })
	if i < 0 {
		return Relation{}, false
	}
	return v.Relations[i], true
}

// Vocabulary is the resolved surface for one caller.
type Vocabulary struct {
	Version string
	Targets []TargetVocabulary
}

// Target answers one record type's vocabulary, and false when the caller may
// not read that type (or when no such type exists — indistinguishable by
// design).
func (v Vocabulary) Target(name string) (TargetVocabulary, bool) {
	i := slices.IndexFunc(v.Targets, func(t TargetVocabulary) bool { return t.Target == name })
	if i < 0 {
		return TargetVocabulary{}, false
	}
	return v.Targets[i], true
}

// TargetNames lists the record types this caller may ask about, in order.
// It is safe to disclose: they are exactly what the caller can already read.
func (v Vocabulary) TargetNames() []string {
	names := make([]string, len(v.Targets))
	for i, t := range v.Targets {
		names[i] = t.Target
	}
	return names
}

// VocabularyResolver composes the vocabulary for whoever is bound to the
// context. It holds no cache: the catalog changes under it (a field is
// retired mid-session) and RBAC changes under it (a passport is demoted), and
// a cached vocabulary would keep answering with the surface as it was.
type VocabularyResolver struct {
	catalog fieldcatalog.Reader
	columns ColumnReader
}

// NewVocabularyResolver builds a resolver over the core contract fields
// alone. Without a field catalog it is still correct, just narrower — the
// same nil-seam pass-through every record store uses.
func NewVocabularyResolver() *VocabularyResolver { return &VocabularyResolver{} }

// WithFieldCatalog wires the custom-field half of the vocabulary.
func (r *VocabularyResolver) WithFieldCatalog(catalog fieldcatalog.Reader) *VocabularyResolver {
	r.catalog = catalog
	return r
}

// WithColumnReader wires the storage half: what the record's own table can
// actually answer (querystorage.go). Without it the vocabulary is the
// contract's — WIDER, never narrower, so an unwired resolver cannot publish
// less than it admits.
func (r *VocabularyResolver) WithColumnReader(columns ColumnReader) *VocabularyResolver {
	r.columns = columns
	return r
}

// Resolve composes the vocabulary for the caller bound to ctx, restricted to
// the named record types (all of them when none are named, which is what the
// published document asks for).
//
// Naming the types matters for cost, not for correctness: the validator
// resolves the one or two a plan actually mentions, so a plan costs at most
// two catalog reads rather than one per searchable entity.
func (r *VocabularyResolver) Resolve(ctx context.Context, targets ...string) (Vocabulary, error) {
	vocab := Vocabulary{Version: PlanVersion}
	// One schema read per table per resolve. An inverse hop is derived from
	// the REFERRING record's column, so composing one record type's vocabulary
	// asks about several tables; without the memo the published document would
	// re-read each of them once per target it lists.
	schema := newSchemaReads(r.columns)
	for _, branch := range searchBranches {
		if len(targets) > 0 && !slices.Contains(targets, branch.entity) {
			continue
		}
		if auth.Require(ctx, branch.entity, principal.ActionRead) != nil {
			continue
		}
		target, err := r.resolveTarget(ctx, schema, branch.entity)
		if err != nil {
			return Vocabulary{}, err
		}
		vocab.Targets = append(vocab.Targets, target)
	}
	return vocab, nil
}

// resolveTarget composes one record type's vocabulary from its contract
// fields, its live custom columns and its derived relations.
func (r *VocabularyResolver) resolveTarget(ctx context.Context, schema *schemaReads, entity string) (TargetVocabulary, error) {
	record, ok := contractRecords[entity]
	if !ok {
		// A searchable entity with no contract binding is a wiring defect,
		// not a caller error: the fitness function fails on it, so reaching
		// here means the check was removed rather than that the plan was bad.
		return TargetVocabulary{}, fmt.Errorf("search: searchable entity %q has no contract record binding", entity)
	}
	fields := contractFields(record)
	custom, err := r.customFields(ctx, entity)
	if err != nil {
		return TargetVocabulary{}, err
	}
	fields = append(fields, custom...)
	// The storage filter runs before the relations are derived, so a hop is
	// only ever derived from a reference the table really holds.
	stored, err := schema.of(ctx, entity)
	if err != nil {
		return TargetVocabulary{}, err
	}
	fields = slices.DeleteFunc(fields, func(f Field) bool { return !stored.answers(f) })
	slices.SortFunc(fields, func(a, b Field) int { return strings.Compare(a.Name, b.Name) })

	inverse, err := storedInverseRelations(ctx, schema, entity, inverseRelations(entity))
	if err != nil {
		return TargetVocabulary{}, err
	}
	relations := append(contractRelations(entity, fields), inverse...)
	relations = r.admittedRelations(ctx, relations)
	slices.SortFunc(relations, func(a, b Relation) int { return strings.Compare(a.Name, b.Name) })

	return TargetVocabulary{Target: entity, Fields: fields, Relations: relations}, nil
}

// storedInverseRelations keeps an inverse hop only when the REFERRING table
// really holds the column the join would run on.
//
// The forward direction is already filtered — its reference is one of the
// target's own fields — but the inverse is derived from another record's
// contract, and the executor joins on that other table's column. A hop
// published from a reference no table holds would validate and then fail as a
// database error, which is the "published but unanswerable" case this whole
// filter exists to remove, one level up from a field.
func storedInverseRelations(ctx context.Context, schema *schemaReads, entity string, candidates []Relation) ([]Relation, error) {
	var kept []Relation
	for _, relation := range candidates {
		stored, err := schema.of(ctx, relation.Target)
		if err != nil {
			return nil, err
		}
		// Via is `<referring type>.<column>`; the column is what the join runs
		// on, and it is an id on the referring record. An UNQUALIFIED Via
		// here is a wiring defect rather than a missing column — the executor
		// reads the same two spellings to decide the join's direction — and
		// dropping the hop for it would hide the defect behind a vocabulary
		// that merely looks narrower than it should be.
		_, column, qualified := strings.Cut(relation.Via, ".")
		if !qualified {
			return nil, fmt.Errorf("search: inverse relation %q on %s carries an unqualified reference %q",
				relation.Name, entity, relation.Via)
		}
		if stored.answers(newField(column, KindID)) {
			kept = append(kept, relation)
		}
	}
	return kept, nil
}

// admittedRelations drops the hops that land on a record type this caller may
// not read. A hop is a read of the record it lands on, so admitting it would
// let a plan filter deals by an organization the caller cannot see — and the
// result count would disclose what the row scope hides.
func (r *VocabularyResolver) admittedRelations(ctx context.Context, relations []Relation) []Relation {
	return slices.DeleteFunc(relations, func(rel Relation) bool {
		return auth.Require(ctx, rel.Target, principal.ActionRead) != nil
	})
}

// customFields reads the workspace's live custom columns for this record type
// through the fieldcatalog seam and gives each the kind its declared type
// maps onto. The wire name IS the column name (`cf_priority`) — the same key
// the record's own JSON carries — so a caller asks for a custom field with
// the name it already reads back.
func (r *VocabularyResolver) customFields(ctx context.Context, entity string) ([]Field, error) {
	if r.catalog == nil {
		return nil, nil
	}
	columns, err := r.catalog.ActiveColumns(ctx, entity)
	if err != nil {
		return nil, fmt.Errorf("search: resolving custom-field vocabulary for %s: %w", entity, err)
	}
	fields := make([]Field, 0, len(columns))
	for _, c := range columns {
		kind, ok := customFieldKinds[c.Type]
		if !ok {
			// A catalog type this vocabulary has no operators for is left
			// OUT rather than admitted with a guessed operator set: an
			// unasked field is a smaller failure than one that answers the
			// wrong comparison.
			continue
		}
		fields = append(fields, newField(c.Name, kind))
	}
	return fields, nil
}

// customFieldKinds maps the six closed custom-field types onto this
// vocabulary's kinds. Both sets are closed and neither is this file's to
// extend: a seventh custom-field type arrives with its own entry, and the
// fitness function fails until it has one.
var customFieldKinds = map[string]FieldKind{
	fieldcatalog.TypeText:     KindText,
	fieldcatalog.TypeNumber:   KindNumber,
	fieldcatalog.TypeDate:     KindDate,
	fieldcatalog.TypeCurrency: KindNumber,
	fieldcatalog.TypePicklist: KindText,
	fieldcatalog.TypeBoolean:  KindBoolean,
}
