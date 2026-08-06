// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package settings is the installation-settings mechanism (ADR-0090/A135):
// one `setting` table holding one row per setting, with the CATALOG in typed
// Go rather than in the schema. The table is persistence; this package owns
// what a setting is.
//
// The split matters. Adding a setting used to cost an ALTER TABLE on
// `workspace` plus an RBAC backfill (0121_capture_auto_enrich, for one
// boolean). Here it costs an Entry — and because the Entry carries its own
// validator, RBAC object and audit verb, none of the governance the column
// form gave up is lost: per-setting audit verbs stay per-setting, and the
// object that gated the column still gates the row.
//
// This package owns the mechanism and no domain. It never learns what a
// currency or a deal is: validators are supplied by the module that owns the
// setting, and compose assembles them (shared → platform → modules →
// compose).
package settings

import (
	"encoding/json"
	"fmt"
)

// Definition is the type-erased view the registry stores. Entry[T] is the
// typed form callers hold; the registry cannot be generic over a
// heterogeneous set, so registration erases and the accessors restore.
type Definition interface {
	// Key is the `<module>.<name>` storage key.
	Key() string
	// Object is the RBAC object gating reads and writes of this setting.
	Object() string
	// AuditVerb is the action written to audit_log on a change. Per-entry so
	// a settings change stays as legible in the ledger as the per-column
	// writes it replaces — one blanket "settings.updated" would be a
	// regression, not a simplification.
	AuditVerb() string
	// DefaultJSON is the value a read resolves to when no row exists.
	DefaultJSON() (json.RawMessage, error)
	// ValidateJSON checks a candidate value, decoding it to the entry's own
	// type first. A value that cannot decode is invalid, not zero.
	ValidateJSON(json.RawMessage) error
	// CanonicalJSON re-encodes a stored value through the entry's own type.
	// The write path compares against this rather than raw bytes: candidates
	// are encoded by Go, stored values come back from Postgres, and jsonb
	// normalization is its choice of key order and whitespace, not ours.
	CanonicalJSON(json.RawMessage) (json.RawMessage, error)
}

// Entry is one setting's declaration: its key, governance, default and
// validation. Modules declare these as package vars; compose registers them.
type Entry[T any] struct {
	key      string
	object   string
	verb     string
	def      T
	validate func(T) error
}

// Define declares a setting. `key` is `<module>.<name>`; the prefix is not
// decoration, a fitness gate asserts it names the module that declares the
// entry. `validate` may be nil when every value of T is admissible.
func Define[T any](key, object, verb string, def T, validate func(T) error) *Entry[T] {
	return &Entry[T]{key: key, object: object, verb: verb, def: def, validate: validate}
}

// Key is the `<module>.<name>` storage key.
func (e *Entry[T]) Key() string { return e.key }

// Object is the RBAC object gating this setting.
func (e *Entry[T]) Object() string { return e.object }

// AuditVerb is the action a change to this setting writes to audit_log.
func (e *Entry[T]) AuditVerb() string { return e.verb }

// DefaultJSON encodes the declared default — the value a read resolves to
// while no row exists.
func (e *Entry[T]) DefaultJSON() (json.RawMessage, error) {
	raw, err := json.Marshal(e.def)
	if err != nil {
		return nil, fmt.Errorf("settings: encoding default for %s: %w", e.key, err)
	}
	return raw, nil
}

// ValidateJSON decodes a candidate to the entry's type and runs the owning
// module's validator over it.
func (e *Entry[T]) ValidateJSON(raw json.RawMessage) error {
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		// The decode failure is "wrong type for this key", not a leak-worthy
		// internal: it names the key and nothing else.
		return InvalidValue{
			Setting: e.key, Code: "setting_type_mismatch",
			Reason: "the value is not the type this setting holds",
		}
	}
	if e.validate == nil {
		return nil
	}
	if err := e.validate(v); err != nil {
		return InvalidValue{Setting: e.key, Code: "setting_invalid", Reason: err.Error()}
	}
	return nil
}

// CanonicalJSON re-encodes a stored value through the entry's type, so the
// write path can tell "unchanged" from "differently spelled".
func (e *Entry[T]) CanonicalJSON(raw json.RawMessage) (json.RawMessage, error) {
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		// A stored value this build cannot decode is not "different" — it is
		// unreadable, and overwriting it would destroy the evidence of
		// whatever wrote it.
		return nil, fmt.Errorf("settings: %s holds a value this build cannot decode: %w", e.key, err)
	}
	out, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("settings: re-encoding %s: %w", e.key, err)
	}
	return out, nil
}

// InvalidValue refuses a setting write whose value the owning module rejects.
// It implements apperrors.FieldFault so the refusal classifies as a 422
// naming the setting wherever it travels — REST and the MCP tool surface
// alike — rather than only on the transport that happens to carry it.
//
// Reason is the OWNING MODULE's sentence. That is the whole point of the
// validator living in the module: "base currency must be ISO-4217" is worth
// saying, and platform could not have said it.
type InvalidValue struct {
	Setting string
	Code    string
	Reason  string
}

func (e InvalidValue) Error() string {
	return fmt.Sprintf("setting %s: %s", e.Setting, e.Reason)
}

// FieldFault names the setting key as the offending field: the caller changes
// a setting by its key, so the key is what they must act on.
func (e InvalidValue) FieldFault() (field, code, message string) {
	return e.Setting, e.Code, e.Reason
}
