// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package search

// The CORE half of the query vocabulary, derived from the generated contract
// types rather than maintained as a list (C-7, SEARCH-AC-15).
//
// The contract is this repo's source of truth (P3), and the generated structs
// ARE the contract — so a vocabulary read off them cannot drift from what the
// API actually returns. A field added upstream is askable the moment the
// contract regenerates; a field removed leaves with it. There is no second
// place to update and therefore no second place to forget.
//
// Everything about a field is derived: its wire name from the json tag, its
// kind from its Go type, its operators from its kind. The only hand-written
// thing here is the BINDING from a searchable entity to its contract type —
// Go reflection cannot enumerate a package's types — and the fitness function
// in queryvocab_test.go fails the moment a searchable entity has no binding.

import (
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

// contractRecords binds each searchable entity to the contract type that
// declares its fields. It is a type binding, not a field list: nothing about
// WHICH fields are askable is decided here.
var contractRecords = map[string]reflect.Type{
	entityPerson:       reflect.TypeOf(crmcontracts.Person{}),
	entityOrganization: reflect.TypeOf(crmcontracts.Organization{}),
	entityDeal:         reflect.TypeOf(crmcontracts.Deal{}),
	entityLead:         reflect.TypeOf(crmcontracts.Lead{}),
	entityProject:      reflect.TypeOf(crmcontracts.Project{}),
	entityActivity:     reflect.TypeOf(crmcontracts.Activity{}),
}

// geoStructs names the contract types that carry a place rather than a value.
// The parent member of one of these contributes a `geo` field (the operand
// `within_radius` takes) on top of the ordinary exact predicates its leaves
// contribute — so `address.city` matches exactly while `address` is the thing
// a radius would be measured from.
var geoStructs = map[reflect.Type]bool{
	reflect.TypeOf(crmcontracts.Address{}): true,
}

// Scalar contract types that are values rather than objects, recognised by
// identity because their Go kinds (struct, array) would otherwise send the
// walk recursing into their internals.
var (
	timeType = reflect.TypeOf(time.Time{})
	dateType = reflect.TypeOf(openapi_types.Date{})
	uuidType = reflect.TypeOf(openapi_types.UUID{})
)

// walkedContracts memoizes the reflection walk: a Go type cannot change at
// runtime, so the walk over one has exactly one answer for the life of the
// process. The CATALOG half of the vocabulary is deliberately NOT cached
// (SEARCH-AC-15 turns on it not being) — the two halves differ because their
// sources do.
var walkedContracts = sync.OnceValue(func() map[reflect.Type][]Field {
	walked := make(map[reflect.Type][]Field, len(contractRecords))
	for _, record := range contractRecords {
		walked[record] = walkContractFields(record)
	}
	return walked
})

// contractFields answers the fields a predicate may name on one contract
// record type.
//
// It CLONES the memoized walk. A caller appends its workspace's custom
// columns to this result and sorts it, and both would otherwise reach through
// the shared backing array into every later caller's vocabulary — one
// workspace's private column becoming another's. The clone is the whole
// defence, and it belongs here rather than at each call site, where the next
// caller would have to know to write it.
func contractFields(t reflect.Type) []Field {
	return slices.Clone(walkedContracts()[t])
}

// walkContractFields is the walk itself. It descends exactly ONE level into a
// nested object, which is what gives `address.city` while keeping the
// vocabulary a flat, finite set. A deeper structure is a shape v1 has no path
// syntax for, so it contributes nothing rather than contributing something
// the validator could not spell.
func walkContractFields(t reflect.Type) []Field {
	var fields []Field
	for i := range t.NumField() {
		member := t.Field(i)
		name, ok := wireName(member)
		if !ok {
			continue
		}
		inner := deref(member.Type)
		if kind, isScalar := scalarKind(inner); isScalar {
			fields = append(fields, newField(name, kind))
			continue
		}
		if inner.Kind() != reflect.Struct {
			// Slices, maps and interfaces are collections and free-form
			// blobs (`emails`, `raw`, `social`). v1 predicates are `field op
			// value` over a single value; there is no operator here that
			// could mean anything against a list.
			continue
		}
		if geoStructs[inner] {
			fields = append(fields, newField(name, KindGeo))
		}
		fields = append(fields, nestedFields(name, inner)...)
	}
	return fields
}

// nestedFields contributes the scalar leaves of a nested object under a
// dotted path. It does not recurse further: one level is the whole of v1's
// path syntax.
func nestedFields(prefix string, t reflect.Type) []Field {
	var fields []Field
	for i := range t.NumField() {
		member := t.Field(i)
		name, ok := wireName(member)
		if !ok {
			continue
		}
		if kind, isScalar := scalarKind(deref(member.Type)); isScalar {
			fields = append(fields, newField(prefix+"."+name, kind))
		}
	}
	return fields
}

// wireName answers the json member name of a struct field, and false for
// anything the wire never carries — an unexported member, or the
// `json:"-"` AdditionalProperties bag every generated record ends with.
func wireName(member reflect.StructField) (string, bool) {
	if !member.IsExported() {
		return "", false
	}
	tag, ok := member.Tag.Lookup("json")
	if !ok {
		return "", false
	}
	name, _, _ := strings.Cut(tag, ",")
	if name == "" || name == "-" {
		return "", false
	}
	return name, true
}

// deref unwraps the pointer the generator puts on every optional member, so
// the walk reasons about the value type in both cases.
func deref(t reflect.Type) reflect.Type {
	if t.Kind() == reflect.Pointer {
		return t.Elem()
	}
	return t
}

// scalarKind maps a Go type onto the closed set of value shapes a predicate
// can carry, and reports false for anything that is not a single value.
// Identity is checked before kind: a UUID is an array and a timestamp a
// struct, and both would otherwise be walked as containers.
func scalarKind(t reflect.Type) (FieldKind, bool) {
	switch t {
	case timeType:
		return KindTimestamp, true
	case dateType:
		return KindDate, true
	case uuidType:
		return KindID, true
	}
	switch t.Kind() {
	case reflect.String:
		return KindText, true
	case reflect.Bool:
		return KindBoolean, true
	case reflect.Int, reflect.Int32, reflect.Int64, reflect.Float32, reflect.Float64:
		return KindNumber, true
	default:
		return "", false
	}
}

// relationSuffix is the contract's own spelling of a reference: a scalar id
// member named `<record type>_id`. Deriving relations from it means the
// traversal set is read off the same contract as the fields, so a new
// reference is traversable the moment it exists.
const relationSuffix = "_id"

// contractRelations answers the depth-1 hops declared BY this record type:
// every scalar id member whose stripped name is itself a searchable record
// type. `deal.organization_id` gives deal the relation `organization`;
// `deal.owner_id` gives nothing, because a user is not a record type this
// module searches.
func contractRelations(entity string, fields []Field) []Relation {
	var relations []Relation
	for _, f := range fields {
		if f.Kind != KindID || !strings.HasSuffix(f.Name, relationSuffix) {
			continue
		}
		target := strings.TrimSuffix(f.Name, relationSuffix)
		if target == entity || contractRecords[target] == nil {
			continue
		}
		relations = append(relations, Relation{Name: target, Target: target, Via: f.Name})
	}
	return relations
}

// inverseRelations answers the hops that land ON this record type: for every
// OTHER searchable record declaring a reference to it, the reverse edge,
// named for the referring type. `deal.organization_id` gives organization the
// relation `deals`.
//
// The inverse direction is derived rather than declared for the same reason
// the forward one is — and it is the direction that carries most of the
// questions worth asking ("organizations with an open deal"), which a
// forward-only derivation would leave unaskable while looking complete.
func inverseRelations(entity string) []Relation {
	var relations []Relation
	for other, t := range contractRecords {
		if other == entity {
			continue
		}
		for _, r := range contractRelations(other, contractFields(t)) {
			if r.Target == entity {
				relations = append(relations, Relation{Name: other + "s", Target: other, Via: other + "." + r.Via})
			}
		}
	}
	return relations
}
