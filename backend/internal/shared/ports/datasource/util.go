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
//craft:ignore naked-any the deserialization seam: the decode target is whichever provider request struct the caller owns
func StrictDecode(raw json.RawMessage, into any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return &FieldDecodeError{Cause: err}
	}
	return nil
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
