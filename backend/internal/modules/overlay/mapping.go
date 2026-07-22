// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

import (
	"fmt"
	"sort"
	"strings"
)

// TargetKind names the shape of an incumbent-CRM property's landing spot
// in the mirror. A 1:1 flat projector can express none of these on its
// own — child rows and jsonb assemblies need their own dispatch
// (design.md §4.8).
type TargetKind int

const (
	// TargetColumn is a 1:1 property → mirror column mapping.
	TargetColumn TargetKind = iota
	// TargetChild is a 1:N mapping into a child row (e.g. person_email):
	// To is "<parent>.<child column>".
	TargetChild
	// TargetAssembler is an N:1 mapping: every From property is gathered
	// into one map[string]any and handed to Transform, which constructs
	// the single To column (typically jsonb, e.g. address/social).
	TargetAssembler
)

// String renders the TargetKind name for error messages; never leaks a
// raw int to a caller trying to diagnose a bad mapping.
func (k TargetKind) String() string {
	switch k {
	case TargetColumn:
		return "column"
	case TargetChild:
		return "child"
	case TargetAssembler:
		return "assembler"
	default:
		return fmt.Sprintf("unknown(%d)", int(k))
	}
}

// FieldMapping is one incumbent property (or, for TargetAssembler,
// property set) → mirror target declaration.
//
// Transform names a function in the closed transform registry, applied
// to the raw value(s) before landing on To. Resolve names an external
// lookup (e.g. owner-id → app_user via mirror_user_map) that Apply
// cannot perform on its own — it has no store access — so a Resolve
// field's raw value passes through unmodified and the actual lookup is
// the ingest layer's job.
type FieldMapping struct {
	From      []string
	To        string
	Kind      TargetKind
	Transform string
	Resolve   string
	// AlwaysEmit forces a TargetAssembler field to run its Transform even
	// when the incumbent record carried NONE of its From properties, so the
	// transform can synthesize a value from nothing — the shape a required,
	// always-present display field with a fallback needs (person.full_name,
	// OVA-MAP-3: never left empty). It is meaningless on the other kinds,
	// whose absence-is-a-no-op behavior is exactly right.
	AlwaysEmit bool
}

// ObjectMapping is the code-declared, test-guarded field map for one
// incumbent object class (e.g. HubSpot "contacts" → Margince "person").
//
// ExternalKey and Baseline are structural, not members of Fields: every
// mapped object carries them, so Apply handles them once rather than
// requiring every ObjectMapping to repeat a FieldMapping for them.
// ExternalKey is the raw property that becomes the mirror's external_id;
// Baseline is the raw property that drives both the incremental-sync
// watermark and the mirror's last_synced_at column.
type ObjectMapping struct {
	Source         string
	Target         string
	ExternalKey    string
	Baseline       string
	Fields         []FieldMapping
	UnmappedPolicy string
	// Const holds target values fixed by the object class itself, not read
	// from any incumbent property — the activity `kind` each of the five
	// HubSpot engagement classes carries (calls→"call", …), which is
	// determined by WHICH class was read, not by a field on the record
	// (OVA-MAP-1). Const values consume no raw property, so they never
	// affect the unmapped set, and a Const key must not collide with any
	// field's To (Apply would otherwise have two writers for one target).
	Const map[string]any
}

// Apply projects a raw incumbent record (a flat properties map, per the
// wire shapes observed in design.md §11) through an ObjectMapping,
// returning the mirror-shaped target map, the list of raw keys that
// matched no declared mapping (UnmappedPolicy governs what the caller
// does with them — "flag" surfaces them, never silently drops per
// UC-E18-01 F3), and an error if the mapping itself is malformed (an
// unknown Transform name, or an unrecognized TargetKind).
func Apply(m ObjectMapping, raw map[string]any) (map[string]any, []string, error) {
	out := map[string]any{}
	consumed := make(map[string]bool, len(raw))

	if m.ExternalKey != "" {
		consumed[m.ExternalKey] = true
		if v, ok := raw[m.ExternalKey]; ok {
			out["external_id"] = v
		}
	}
	if m.Baseline != "" {
		consumed[m.Baseline] = true
		if v, ok := raw[m.Baseline]; ok {
			out["last_synced_at"] = v
		}
	}

	for k, v := range m.Const {
		if _, clash := out[k]; clash {
			return nil, nil, fmt.Errorf("overlay: const target %q collides with a structural key", k)
		}
		out[k] = v
	}

	for _, f := range m.Fields {
		for _, k := range f.From {
			consumed[k] = true
		}
		if _, clash := m.Const[f.To]; clash {
			return nil, nil, fmt.Errorf("overlay: field target %q collides with a const target", f.To)
		}
		if err := applyField(out, f, raw); err != nil {
			return nil, nil, err
		}
	}

	var unmapped []string
	for k := range raw {
		if !consumed[k] {
			unmapped = append(unmapped, k)
		}
	}
	sort.Strings(unmapped)

	return out, unmapped, nil
}

// applyField computes one FieldMapping's projected value and writes it
// into out at the shape its TargetKind dictates. It is a no-op (not an
// error) when the incumbent record never sent any of the field's From
// properties — the mirror simply carries no value for that column yet.
func applyField(out map[string]any, f FieldMapping, raw map[string]any) error {
	val, present, err := valueFor(f, raw)
	if err != nil {
		return err
	}
	if !present {
		return nil
	}

	switch f.Kind {
	case TargetColumn:
		out[f.To] = val
	case TargetChild:
		parent, child, ok := strings.Cut(f.To, ".")
		if !ok {
			return fmt.Errorf("overlay: child target %q must be \"<parent>.<child column>\"", f.To)
		}
		childRow, _ := out[parent].(map[string]any)
		if childRow == nil {
			childRow = map[string]any{}
		}
		childRow[child] = val
		out[parent] = childRow
	case TargetAssembler:
		out[f.To] = val
	default:
		return fmt.Errorf("overlay: unknown target kind %s for field %q", f.Kind, f.To)
	}
	return nil
}

// valueFor computes one FieldMapping's projected value. present is false
// when every one of the field's From keys is absent from raw (or, for a
// single-value field, present but the empty string HubSpot uses for an
// unset property), so Apply can skip emitting a target for data the
// incumbent record never sent.
//
//craft:ignore naked-any raw is a parsed-JSON incumbent record and the return is its mapped JSON value — the untyped data boundary
func valueFor(f FieldMapping, raw map[string]any) (any, bool, error) {
	if f.Kind == TargetAssembler {
		gathered := make(map[string]any, len(f.From))
		present := false
		for _, k := range f.From {
			if v, ok := raw[k]; ok {
				gathered[k] = v
				present = true
			}
		}
		if !present && !f.AlwaysEmit {
			return nil, false, nil
		}
		return applyTransform(f, gathered)
	}

	if len(f.From) != 1 {
		return nil, false, fmt.Errorf("overlay: %s target %q must declare exactly one From property, got %d", f.Kind, f.To, len(f.From))
	}
	v, ok := raw[f.From[0]]
	if !ok {
		return nil, false, nil
	}
	if s, isString := v.(string); isString && s == "" {
		return nil, false, nil
	}
	return applyTransform(f, v)
}

// applyTransform runs v through the field's Transform, if any. An
// unknown Transform name is a mapping-declaration error, never a silent
// passthrough. A Resolve field carries its raw value through as-is —
// Apply has no store access to perform the lookup itself.
//
//craft:ignore naked-any v/the return value are decoded incumbent values flowing through the closed transform registry, not a missed type
func applyTransform(f FieldMapping, v any) (any, bool, error) {
	if f.Transform == "" {
		return v, true, nil
	}
	fn, ok := transforms[f.Transform]
	if !ok {
		return nil, false, fmt.Errorf("overlay: unknown transform %q declared on field %q", f.Transform, f.To)
	}
	out, err := fn(v)
	if err != nil {
		return nil, false, err
	}
	return out, true, nil
}
