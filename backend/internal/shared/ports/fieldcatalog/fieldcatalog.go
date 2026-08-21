// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package fieldcatalog is the cross-module seam a record store rides to
// consume custom-field columns without importing modules/customfields
// directly (ADR-0054 §3: "a module NEVER imports a sibling"). The
// catalog engine (modules/customfields) owns the custom_field table and
// implements Reader; compose injects the concrete Reader into
// person/organization/deal store constructors — a nil Reader is the
// zero-cost pass-through a store falls back to when the seam is unwired
// (tests, or a deployment that never mounted the module).
//
// Column is deliberately thin: just enough for a record store's SQL
// mechanics (platform/database/storekit's customcolumns.go helpers) to
// build a SELECT/INSERT/UPDATE fragment and convert a wire value to and
// from its bind shape. Admin-facing catalog metadata (slug, label,
// lifecycle status, picklist options, …) stays inside modules/customfields
// — a record store has no business with it.
package fieldcatalog

import "context"

// The six closed field types (custom-fields.md), spelled the way
// modules/customfields' own type constants and the custom_field.type
// CHECK constraint spell them. Shared may not import modules (it would
// invert the shared → platform → modules DAG), so this is the one other
// place these six literals are allowed to live — modules/customfields
// and platform/database/storekit both consume this set rather than
// hand-rolling their own copies.
const (
	TypeText     = "text"
	TypeNumber   = "number"
	TypeDate     = "date"
	TypeCurrency = "currency"
	TypePicklist = "picklist"
	TypeBoolean  = "boolean"
)

// Types answers the closed set above, so a consumer that has to handle EVERY
// field type derives that obligation instead of restating it. A gate written
// over a hand-copied list of the six passes unchanged the day a seventh is
// added here, which is the one moment it exists to fail.
//
// What such a gate protects is not uniform. Missing the segment or search
// vocabulary costs a column its filter; missing storekit's conversion matrix
// costs the VALUE — SQLValue and extractValue drop an unrecognised type on
// both the write and the read.
//
// A fresh slice per call: the alternative is an exported package-level slice,
// which any consumer can reorder or overwrite for every other consumer.
func Types() []string {
	return []string{TypeText, TypeNumber, TypeDate, TypeCurrency, TypePicklist, TypeBoolean}
}

// Column is one custom-field column for a (workspace, object) pair,
// identified by its physical column name and its closed field type (one
// of the Type* constants above). Whether a given Column is active,
// retired, or both is a question of which method returned it — Reader
// and FilterableReader below — not of the type itself.
//
// The fields carry DIFFERENT disclosure rules, and this is the one place that
// says so, because three surfaces read them and each was choosing for itself:
//
//   - Name and Type are SCHEMA. Ambient to any caller who may read records of
//     that object, because a consumer that had to hide them would describe a
//     narrower product than the engine implements — a field nothing may name is
//     a field a filter cannot use. Note what this does NOT rest on: a record
//     payload omits a NULL, so a column with no value on any record is not
//     already visible there. Ambient is a decision, not an observation.
//   - Options is catalogue CONTENT, authored by an admin. A consumer passing it
//     to a caller needs `custom_field:read`, the grant that governs the
//     catalogue surface these values otherwise come from.
//
// Neither of those is a Column's own business to enforce — it holds no context —
// so the obligation lands on the consumer, which is why it is written where every
// consumer reads rather than in each of them.
type Column struct {
	Name string
	Type string
	// Options is a picklist column's allowed values, and is empty for every
	// other type. It travels with the column because a consumer that has to
	// OFFER the field needs them — a builder without them can only ask a reader
	// to type a value from a closed set, which is how a mistyped one becomes a
	// filter that silently matches nothing.
	//
	// The catalogue owns them, as it owns labels: they are per-workspace admin
	// state, not something the engine or a consumer may derive.
	Options []string
}

// Reader answers the active custom-field columns for one core object,
// scoped to the workspace bound to ctx. Implemented by
// modules/customfields' Service; a record store calls it once per
// operation (Get/List/Create/Update) to learn which cf_* columns
// participate, then drives platform/database/storekit's customcolumns.go
// helpers with the result — the store itself never touches the
// custom_field catalog table.
type Reader interface {
	ActiveColumns(ctx context.Context, object string) ([]Column, error)
}

// FilterableReader answers the columns a FILTER may name, which is a different
// question from the ones a write may set: a retired field keeps its column and
// its values, so a saved segment built on it must keep evaluating, while nothing
// may write to it again. It is its own interface rather than a second method on
// Reader because a consumer of one has no use for the other — collections filters
// and never writes cf_* values, and the record stores write and never filter.
type FilterableReader interface {
	FilterableColumns(ctx context.Context, object string) ([]Column, error)
}
