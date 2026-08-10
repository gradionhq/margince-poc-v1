// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package settings names the installation settings that cross module
// boundaries (ADR-0090/A135).
//
// A setting is OWNED by one module — the one that declares its type, default,
// validator, RBAC object and audit verb — but the installation-wide ones are
// read by several, and a module never imports a sibling (ADR-0054 §3). So the
// NAME lives here, once, as a typed key: the owning module binds its entry to
// it, and every other module reads through it.
//
// Only keys, deliberately. The mechanism is platform/settings, which modules
// may import directly since platform sits below them in the DAG. What they
// cannot do is reach into identity for the name, which is all this package
// supplies.
//
// A setting only its owner reads does not belong here — the owner holds its
// entry directly, and padding this list would hide how small the real coupling
// is.
package settings

// Key names one setting and binds it to the Go type its value decodes to.
type Key[T any] struct{ name string }

// Name is the wire and storage key, `<module>.<name>`.
func (k Key[T]) Name() string { return k.name }

// NewKey mints a key. Exported because platform/settings has to build the same
// key an owning module's entry is declared with; nothing else should call it.
func NewKey[T any](name string) Key[T] { return Key[T]{name: name} }

// The installation-wide settings read across module boundaries. Identity owns
// all three — it owns the installation — and declares their entries.
var (
	// InstallationName is the organization's display name.
	InstallationName = NewKey[string]("installation.name")

	// InstallationBaseCurrency is the ISO-4217 base every money roll-up
	// converts to. Frozen once a deal has converted against it (ADR-0085 §7).
	InstallationBaseCurrency = NewKey[string]("installation.base_currency")

	// InstallationTimezone is the IANA zone every reporting period boundary is
	// computed in — not a user's own display zone, which is per-user.
	InstallationTimezone = NewKey[string]("installation.timezone")
)
