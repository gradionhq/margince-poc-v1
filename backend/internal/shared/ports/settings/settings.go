// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package settings is the cross-module seam for reading installation
// settings (ADR-0090/A135). A setting is owned by exactly one module — the
// module that declares its type, default, validator, RBAC object and audit
// verb — but several modules READ the installation-wide ones, and a module
// never imports a sibling (ADR-0054 §3). So the NAME of every setting lives
// here, once, as a typed Key; the owning module binds its entry to that key,
// and every other module reads through the Reader compose injects.
//
// Shape follows ports/fieldcatalog: a thin interface the owning module
// implements, a nil value meaning "unwired" for tests and deployments that
// never mounted the module. Raw is the only method because Go interfaces
// cannot carry generic methods — Read[T] is the typed accessor callers use,
// and the type parameter is supplied by the Key, so a caller never names it.
package settings

import (
	"context"
	"encoding/json"
	"fmt"
)

// Key names one setting and binds it to the Go type its value decodes to.
// Declared here rather than in the owning module so a reader does not import
// a sibling; the owning module's entry references the same Key, so the name
// has exactly one definition site.
type Key[T any] struct{ name string }

// Name is the wire/storage key, `<module>.<name>`. The module prefix is not
// decoration: a fitness test asserts the prefix matches the module that
// declares the entry, which is what keeps ownership legible.
func (k Key[T]) Name() string { return k.name }

// define is unexported so the cross-module set below is closed — a setting
// several modules read is a line in this file plus an entry in its owning
// module, never an ad-hoc string at a call site.
func define[T any](name string) Key[T] { return Key[T]{name: name} }

// NewLocalKey mints a key for a setting only its owning module reads. Those
// names deliberately do NOT appear in the list below: that list is the
// cross-module surface, and padding it with keys nobody outside reads would
// hide how small the real coupling is. The registry still holds every key, so
// the completeness and prefix-ownership gates cover local settings too.
func NewLocalKey[T any](name string) Key[T] { return Key[T]{name: name} }

// The installation-wide settings read across module boundaries.
//
// Settings read ONLY by their owning module do not need a Key here — that
// module holds its own entry directly. This list is exactly the cross-module
// surface, which is why it is short and expected to stay so.
var (
	// InstallationName is the organization's display name (ADR-0061 §2 seeds
	// it from margince.yaml at bootstrap; thereafter the row is authoritative).
	InstallationName = define[string]("installation.name")

	// InstallationBaseCurrency is the ISO-4217 base every money roll-up
	// converts to. Guarded: ADR-0085 §7 owns the refusal predicate (once a
	// deal has frozen a conversion rate against it), this seam only reads.
	InstallationBaseCurrency = define[string]("installation.base_currency")

	// InstallationTimezone is the IANA reporting-period zone.
	InstallationTimezone = define[string]("installation.timezone")
)

// Reader is the read side of the settings store. The concrete implementation
// is platform/settings.Store; compose injects it into every module that reads
// a setting it does not own.
type Reader interface {
	// Raw returns the stored value for a key as JSON, or the registered
	// default when no row exists. An unregistered key is an error, never a
	// zero value — a typo must not read as "unset".
	Raw(ctx context.Context, key string) (json.RawMessage, error)
}

// Read resolves a typed setting through the seam. A nil Reader is a
// programming error rather than a pass-through: unlike a custom-field
// catalog, there is no meaningful "no settings" fallback — the caller asked
// for a value that governs behaviour, and guessing one would be worse than
// failing.
func Read[T any](ctx context.Context, r Reader, key Key[T]) (T, error) {
	var zero T
	if r == nil {
		return zero, fmt.Errorf("settings: reading %s: no reader wired", key.name)
	}
	raw, err := r.Raw(ctx, key.name)
	if err != nil {
		return zero, err
	}
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		return zero, fmt.Errorf("settings: decoding %s: %w", key.name, err)
	}
	return out, nil
}
