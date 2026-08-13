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
	// TargetChild is a 1:N mapping into a child collection (e.g.
	// person_email): To is "<parent>.<child column>" and the field's ChildRow
	// says which row of that collection it lands on. The parent key always
	// holds a collection, one row per declared ChildRow the incumbent
	// populated.
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
	// Child declares the row a TargetChild field lands on. Required on that
	// kind and meaningless on the others.
	Child *ChildRow
}

// ChildRow declares which row of a child collection a TargetChild field lands
// on. A contact's work email and mobile number are different rows of one
// collection, and nothing in the incumbent property itself says which is which
// — the mapping does. Attrs are the row's fixed members (the type
// discriminator, the primary flag), which no incumbent property supplies.
// Position is the row's place in the collection, unique among the rows of one
// parent and carried into the payload, so a consumer that regroups the rows
// orders them by what the mapping declared rather than by the order it
// happened to traverse them in.
type ChildRow struct {
	Attrs    map[string]any
	Position int
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

// keyExternalID/keyLastSyncedAt are the two structural mirror targets Apply
// writes directly (from ExternalKey/Baseline) — reserved, so a mapping's
// Const may never claim them.
const (
	keyExternalID   = "external_id"
	keyLastSyncedAt = "last_synced_at"
)

// childPositionKey carries a child row's declared order into the mirror
// payload. Apply orders each collection by it, and it survives into the payload
// so a consumer that regroups the rows — after the jsonb round trip, or across
// a wire — restores the order the mapping fixed rather than the order it
// happened to traverse them in.
const childPositionKey = "position"

// Apply projects a raw incumbent record (a flat properties map, per the
// wire shapes observed in design.md §11) through an ObjectMapping,
// returning the mirror-shaped target map, the list of raw keys that
// matched no declared mapping (UnmappedPolicy governs what the caller
// does with them — "flag" surfaces them, never silently drops per
// UC-E18-01 F3), and an error if the mapping itself is malformed (an
// unknown Transform name, an unrecognized TargetKind, child rows of one
// parent that collide, or two fields writing one target).
func Apply(m ObjectMapping, raw map[string]any) (map[string]any, []string, error) {
	if err := checkChildRowDeclarations(m); err != nil {
		return nil, nil, err
	}
	if err := checkTargetCollisions(m); err != nil {
		return nil, nil, err
	}

	out := map[string]any{}
	consumed := make(map[string]bool, len(raw))
	childParents := map[string]bool{}

	if m.ExternalKey != "" {
		consumed[m.ExternalKey] = true
		if v, ok := raw[m.ExternalKey]; ok {
			out[keyExternalID] = v
		}
	}
	if m.Baseline != "" {
		consumed[m.Baseline] = true
		if v, ok := raw[m.Baseline]; ok {
			out[keyLastSyncedAt] = v
		}
	}

	for k, v := range m.Const {
		// Reserved structural targets are checked by NAME, not by whether this
		// particular record happened to populate them — a Const must never
		// substitute the mirror's identity or watermark.
		if k == keyExternalID || k == keyLastSyncedAt {
			return nil, nil, fmt.Errorf("overlay: const target %q collides with a structural key", k)
		}
		out[k] = v
	}

	for _, f := range m.Fields {
		for _, k := range f.From {
			consumed[k] = true
		}
		// A TargetChild writes its PARENT key (out["person_email"]), not the
		// dotted To — check the parent so a Const at that key is caught.
		constTarget := f.To
		if f.Kind == TargetChild {
			constTarget, _, _ = strings.Cut(f.To, ".")
			childParents[constTarget] = true
		}
		if _, clash := m.Const[constTarget]; clash {
			return nil, nil, fmt.Errorf("overlay: field target %q collides with a const target", f.To)
		}
		if err := applyField(out, f, raw); err != nil {
			return nil, nil, err
		}
	}
	for parent := range childParents {
		sortChildRowsByPosition(out, parent)
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

// checkChildRowDeclarations rejects a mapping whose child rows are malformed or
// collide, independently of what any one record happens to carry. applyField
// returns early for a property the incumbent did not send, so a check made
// while projecting would fire only when the offending property happened to be
// populated — a defect that surfaces on particular data only is a defect that
// reaches production and waits.
func checkChildRowDeclarations(m ObjectMapping) error {
	positions := map[string]map[int]bool{}
	for _, f := range m.Fields {
		if f.Kind != TargetChild {
			continue
		}
		parent, err := checkChildRow(f)
		if err != nil {
			return err
		}
		seen := positions[parent]
		if seen == nil {
			seen = map[int]bool{}
			positions[parent] = seen
		}
		if seen[f.Child.Position] {
			return fmt.Errorf("overlay: two child rows of %q both claim position %d; the collection's order would be arbitrary — give each row of one parent its own position", parent, f.Child.Position)
		}
		seen[f.Child.Position] = true
	}
	return nil
}

// checkTargetCollisions rejects a mapping whose fields would write one target
// twice. applyField writes each field into the target map in declaration order,
// so a second writer is no error while projecting — it simply wins, taking
// either the first field's column value or, where a column lands on a child
// collection's parent key, every row of that collection with it. The check runs
// on the declaration for the same reason the child-row one does: a record
// carrying neither property projects cleanly, so a collision found while
// projecting is a defect that reaches production and waits for the data that
// populates both.
//
// Rows of ONE child collection are the shape the child kind exists for: they
// share a parent key by design, and checkChildRowDeclarations already keeps
// each row's position within it unique.
func checkTargetCollisions(m ObjectMapping) error {
	writers := make(map[string]FieldMapping, len(m.Fields))
	for _, f := range m.Fields {
		target := f.To
		if f.Kind == TargetChild {
			target, _, _ = strings.Cut(f.To, ".")
		}
		first, claimed := writers[target]
		if !claimed {
			writers[target] = f
			continue
		}
		if first.Kind == TargetChild && f.Kind == TargetChild {
			continue
		}
		return fmt.Errorf("overlay: %s target %q (from %v) and %s target %q (from %v) both write %q, and the "+
			"second silently replaces the first; give one of them a target of its own, or, where the two are rows "+
			"of one collection, declare both as child targets with their own positions",
			first.Kind, first.To, first.From, f.Kind, f.To, f.From, target)
	}
	return nil
}

// checkChildRow validates one TargetChild field's row declaration and answers
// the parent collection its row lands in.
func checkChildRow(f FieldMapping) (string, error) {
	parent, child, ok := strings.Cut(f.To, ".")
	if !ok {
		return "", fmt.Errorf("overlay: child target %q must be \"<parent>.<child column>\"", f.To)
	}
	if f.Child == nil {
		return "", fmt.Errorf("overlay: child target %q declares no ChildRow, so the row it lands on is undeclared; give it a ChildRow with the row's position and its fixed attributes", f.To)
	}
	for k := range f.Child.Attrs {
		if k == child || k == childPositionKey {
			return "", fmt.Errorf("overlay: child target %q declares an attribute %q that the row already owns; drop it from Attrs and let the mapped column and the declared position supply it", f.To, k)
		}
	}
	return parent, nil
}

// sortChildRowsByPosition orders one child collection by the position each row
// declares, so a consumer reading the rows in slice order reads the order the
// mapping fixed rather than the order the fields happen to be declared in.
// checkChildRowDeclarations makes the positions unique within a parent, so the
// order is total.
func sortChildRowsByPosition(out map[string]any, parent string) {
	rows, ok := out[parent].([]map[string]any)
	if !ok {
		return
	}
	sort.SliceStable(rows, func(i, j int) bool {
		left, _ := rows[i][childPositionKey].(int)
		right, _ := rows[j][childPositionKey].(int)
		return left < right
	})
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
		// Apply validates every child declaration before it projects the first
		// record, so the target is dotted, the ChildRow is there, and its
		// attributes claim no member the row already owns.
		parent, child, _ := strings.Cut(f.To, ".")
		row := map[string]any{child: val, childPositionKey: f.Child.Position}
		for k, v := range f.Child.Attrs {
			row[k] = v
		}
		rows, _ := out[parent].([]map[string]any)
		out[parent] = append(rows, row)
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
