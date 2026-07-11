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

// Column is one active custom-field column for a (workspace, object)
// pair, as a record store needs to see it: its physical column name and
// its closed field type (one of the Type* constants above).
type Column struct {
	Name string
	Type string
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
