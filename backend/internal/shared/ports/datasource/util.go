// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package datasource

// Provider-side mechanics every SystemOfRecordProvider implementation
// needs (the SoR-mode modules today, an overlay adapter tomorrow):
// strict field decoding, payload normalization, record marshalling, and
// the two typed errors the transports map to 422. Additive utilities
// only — the seam interfaces above stay frozen (ADR-0054 §4).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"
)

// UnsupportedEntityError maps to 422 on every surface.
type UnsupportedEntityError struct{ Type string }

func (e *UnsupportedEntityError) Error() string {
	return "entity_type " + e.Type + " is not person|organization|deal|lead|activity"
}

// FieldDecodeError maps to 422 on every surface.
type FieldDecodeError struct{ Cause error }

func (e *FieldDecodeError) Error() string { return "fields: " + e.Cause.Error() }
func (e *FieldDecodeError) Unwrap() error { return e.Cause }

// RawFields normalizes the seam's `any` payload: tools hand over the
// agent's raw JSON; in-process callers may hand the typed request struct.
//
//craft:ignore naked-any the frozen seam's Fields payload is declared any (ports/datasource) — this is the one normalizer of that seam
func RawFields(v any) (json.RawMessage, error) {
	switch f := v.(type) {
	case json.RawMessage:
		return f, nil
	case []byte:
		return f, nil
	default:
		return json.Marshal(v)
	}
}

// StrictDecode rejects unknown fields — an agent misspelling an argument
// gets a 422 naming it, never a silent drop.
//
// It also refuses a field key that is not a BYTE-EXACT match for a
// contract field name. encoding/json matches struct fields
// case-insensitively, so `{"FULL_NAME":…}` would otherwise decode into
// `full_name` and write the column — and the human-edit-precedence probe
// (compose), which matches audit keys case-sensitively in jsonb, would
// have cleared that same patch as touching no human-owned field. The two
// must agree on key identity or an agent smuggles a human-owned overwrite
// through the 🟢 path under a differently-cased key. Types that carry an
// AdditionalProperties catch-all own their own key policy (they route and
// drop non-exact keys) and are left to it.
//
//craft:ignore naked-any the deserialization seam: the decode target is whichever provider request struct the caller owns
func StrictDecode(raw json.RawMessage, into any) error {
	if err := RejectNonCanonicalKeys(raw, into); err != nil {
		return &FieldDecodeError{Cause: err}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return &FieldDecodeError{Cause: err}
	}
	return nil
}

// RejectNonCanonicalKeys enforces byte-exact field-key identity for target
// structs with no AdditionalProperties catch-all. A catch-all field
// (json:"-" map) signals the type accepts arbitrary keys and drops the
// non-exact ones itself, so exact-key enforcement is left to it. Both
// transports validate through this ONE function — the provider seam
// (StrictDecode) and the REST body decode (platform/httperr) — so a
// case-variant key cannot be a field patch on one surface and a rejected
// key on the other.
//
//craft:ignore naked-any mirror of StrictDecode's seam target
func RejectNonCanonicalKeys(raw json.RawMessage, into any) error {
	t := reflect.TypeOf(into)
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil || t.Kind() != reflect.Struct {
		return nil
	}
	canonical := make(map[string]struct{})
	if collectCanonicalKeys(t, canonical) {
		// A catch-all map field: the type owns its own key policy.
		return nil
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		// Not a JSON object — the decoder below reports the shape error.
		return nil
	}
	for key := range keys {
		if _, ok := canonical[key]; !ok {
			return fmt.Errorf("unknown field %q", key)
		}
	}
	return nil
}

// collectCanonicalKeys gathers the byte-exact json field names of a struct
// into out, following encoding/json's own rules: an embedded struct
// promotes its fields, so it is walked recursively. It returns true if the
// type carries an AdditionalProperties catch-all — a `json:"-"` MAP field,
// the shape oapi-codegen generates for `additionalProperties` — in which
// case the type owns its key policy and exact-key enforcement is off. The
// catch-all test is the field's kind, not just its tag: a plain `json:"-"`
// (an internal ignored field, a common idiom) must NOT silently disable
// the backstop for the whole request type.
func collectCanonicalKeys(t reflect.Type, out map[string]struct{}) (hasCatchAll bool) {
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		tag := field.Tag.Get("json")
		name := tag
		if comma := strings.IndexByte(tag, ','); comma >= 0 {
			name = tag[:comma]
		}
		if name == "-" {
			if field.Type.Kind() == reflect.Map {
				return true
			}
			continue
		}
		if field.Anonymous && name == "" {
			et := field.Type
			for et.Kind() == reflect.Pointer {
				et = et.Elem()
			}
			if et.Kind() == reflect.Struct && collectCanonicalKeys(et, out) {
				return true
			}
			continue
		}
		if name != "" {
			out[name] = struct{}{}
		}
	}
	return false
}

// NewRecord marshals a provider's typed fields into the seam's Record
// shape. SoR-mode freshness is trivially authoritative: there is no
// mirror to go stale.
//
//craft:ignore naked-any provider field shapes are per-entity structs serialized into the seam's schemaless Record.Fields
func NewRecord(ref EntityRef, fields any, version *int64) (Record, error) {
	raw, err := json.Marshal(fields)
	if err != nil {
		return Record{}, err
	}
	rec := Record{Ref: ref, Fields: raw, Freshness: FreshnessInfo{LastSyncedAt: time.Now().UTC(), Authoritative: true}}
	if version != nil {
		rec.Version = *version
	}
	return rec, nil
}
