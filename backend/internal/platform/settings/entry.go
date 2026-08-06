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
// currency or a deal is: validators and freeze probes are supplied by the
// module that owns the setting, and compose assembles them (shared →
// platform → modules → compose).
package settings

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	portsettings "github.com/gradionhq/margince/backend/internal/shared/ports/settings"
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
	// Frozen reports whether the setting has become immutable, and why. The
	// reason is the owning module's own sentence, so the refusal explains
	// itself rather than denying generically.
	Frozen(context.Context, pgx.Tx) (bool, string, error)
}

// Entry is one setting's declaration: its key, governance, default,
// validation, and optional freeze probe. Modules declare these as package
// vars; compose registers them.
type Entry[T any] struct {
	key      portsettings.Key[T]
	object   string
	verb     string
	def      T
	validate func(T) error
	freeze   func(context.Context, pgx.Tx) (bool, string, error)
}

// Define declares a setting bound to a cross-module Key. Use this for the
// installation-wide settings other modules read through
// ports/settings.Reader; the Key is what keeps the name defined once.
func Define[T any](key portsettings.Key[T], object, verb string, def T, validate func(T) error) *Entry[T] {
	return &Entry[T]{key: key, object: object, verb: verb, def: def, validate: validate}
}

// DefineLocal declares a setting only its owning module reads. It mints the
// Key here rather than in the port, so the port's list stays exactly the
// cross-module surface and does not accumulate names nobody outside reads.
func DefineLocal[T any](name, object, verb string, def T, validate func(T) error) *Entry[T] {
	return Define(portsettings.NewLocalKey[T](name), object, verb, def, validate)
}

// WithFreeze attaches an immutability probe, supplied by the owning module so
// this package never learns the domain predicate. Returns the entry for
// declaration-site chaining.
func (e *Entry[T]) WithFreeze(probe func(context.Context, pgx.Tx) (bool, string, error)) *Entry[T] {
	e.freeze = probe
	return e
}

// TypedKey exposes the typed key so a module can read its own setting without
// restating the name.
func (e *Entry[T]) TypedKey() portsettings.Key[T] { return e.key }

// Key is the `<module>.<name>` storage key.
func (e *Entry[T]) Key() string { return e.key.Name() }

// Object is the RBAC object gating this setting.
func (e *Entry[T]) Object() string { return e.object }

// AuditVerb is the action a change to this setting writes to audit_log.
func (e *Entry[T]) AuditVerb() string { return e.verb }

// DefaultJSON encodes the declared default — the value a read resolves to
// while no row exists.
func (e *Entry[T]) DefaultJSON() (json.RawMessage, error) {
	raw, err := json.Marshal(e.def)
	if err != nil {
		return nil, fmt.Errorf("settings: encoding default for %s: %w", e.Key(), err)
	}
	return raw, nil
}

// ValidateJSON decodes a candidate to the entry's type and runs the owning
// module's validator over it.
func (e *Entry[T]) ValidateJSON(raw json.RawMessage) error {
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		// The decode error is the wrong type for this key, not a leak-worthy
		// internal: it names the key and the expected shape, nothing else.
		return InvalidValue{
			Setting: e.Key(), Code: "setting_type_mismatch",
			Reason: "the value is not the type this setting holds",
		}
	}
	if e.validate == nil {
		return nil
	}
	if err := e.validate(v); err != nil {
		return InvalidValue{Setting: e.Key(), Code: "setting_invalid", Reason: err.Error()}
	}
	return nil
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

// Frozen runs the owning module's immutability probe, if it declared one.
func (e *Entry[T]) Frozen(ctx context.Context, tx pgx.Tx) (bool, string, error) {
	if e.freeze == nil {
		return false, "", nil
	}
	return e.freeze(ctx, tx)
}
