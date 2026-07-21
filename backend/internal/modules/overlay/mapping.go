// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

import (
	"fmt"
	"math"
	"sort"
	"strconv"
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
}

// transforms is the CLOSED registry (design.md §4.8): mapping.yaml/Go
// declarations only ever *select* a name from this set, never express
// inline logic. Apply rejects any Transform name absent here — never a
// silent passthrough.
// The transform registry seam operates on untyped incoming JSON property
// values: an incumbent record's property is any of string/number/object,
// so a transform takes `any` and asserts its concrete type in its first
// lines (returning a typed error on mismatch). The `any` is the data
// boundary itself, not a missing type — hence the per-transform waivers.
var transforms = map[string]func(any) (any, error){
	"lowercase":              transformLowercase,
	"amount_to_minor":        transformAmountToMinor,
	"employees_to_size_band": transformEmployeesToSizeBand,
	"address_json":           transformAddressJSON,
}

//craft:ignore naked-any transform seam over untyped incoming JSON property values; asserts concrete type within
func transformLowercase(v any) (any, error) {
	s, ok := v.(string)
	if !ok {
		return nil, fmt.Errorf("overlay: lowercase transform expects a string, got %T", v)
	}
	return strings.ToLower(s), nil
}

// transformAmountToMinor parses a HubSpot decimal-string amount (e.g.
// "1234.5") into integer minor units (cents), rounding half away from
// zero. HubSpot always renders numeric properties as strings; valueFor
// already treats an empty string as an absent property, so this never
// sees "".
//
// math.Round (not the "+0.5 then truncate" idiom) is required here:
// int64(f*100+0.5) rounds toward zero for a NEGATIVE f — e.g.
// -12.567*100+0.5 = -1256.2, and int64() truncates that toward zero to
// -1256, when the nearest minor-unit value is -1257. math.Round rounds
// half away from zero for both signs, which a deal's amount (a signed
// quantity — a refund/credit line can be negative) requires.
//
//craft:ignore naked-any transform seam over untyped incoming JSON property values; asserts concrete type within
func transformAmountToMinor(v any) (any, error) {
	s, ok := v.(string)
	if !ok {
		return nil, fmt.Errorf("overlay: amount_to_minor transform expects a string, got %T", v)
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil, fmt.Errorf("overlay: amount_to_minor could not parse %q as a decimal amount", s)
	}
	return int64(math.Round(f * 100)), nil
}

// transformEmployeesToSizeBand buckets a HubSpot numberofemployees
// decimal-string into the mirror's size_band enum. valueFor already
// treats an empty string as an absent property, so this never sees "".
//
//craft:ignore naked-any transform seam over untyped incoming JSON property values; asserts concrete type within
func transformEmployeesToSizeBand(v any) (any, error) {
	s, ok := v.(string)
	if !ok {
		return nil, fmt.Errorf("overlay: employees_to_size_band transform expects a string, got %T", v)
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return nil, fmt.Errorf("overlay: employees_to_size_band could not parse %q as an integer", s)
	}
	switch {
	case n <= 10:
		return "1-10", nil
	case n <= 50:
		return "11-50", nil
	case n <= 200:
		return "51-200", nil
	case n <= 1000:
		return "201-1000", nil
	default:
		return "1001+", nil
	}
}

// transformAddressJSON assembles the address property set into the
// mirror's jsonb address column, dropping properties absent from the
// incumbent record.
//
//craft:ignore naked-any transform seam over untyped incoming JSON property values; asserts concrete type within
func transformAddressJSON(v any) (any, error) {
	fields, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("overlay: address_json transform expects a property map, got %T", v)
	}
	out := make(map[string]any, len(fields))
	for k, val := range fields {
		s, ok := val.(string)
		if ok && s == "" {
			continue
		}
		if val == nil {
			continue
		}
		out[k] = val
	}
	return out, nil
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

	for _, f := range m.Fields {
		for _, k := range f.From {
			consumed[k] = true
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
		if !present {
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
