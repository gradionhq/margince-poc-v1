// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package jurisdiction carries the jurisdiction-pack contract of the
// published extension surface (ADR-0069 §3, ADR-0042): a pack supplies
// country-specific POLICY the core engines consult — it is never an
// actor. These types are frozen published API from their first external
// consumer; they evolve additively or through versioned successors,
// never in place (EXT-P3).
//
// Deliberately absent: locale/i18n. Locale is per-user and orthogonal to
// jurisdiction (A57) — there is no LocaleBundle on this seam.
package jurisdiction

// Pack is one jurisdiction's compiled-in behavior set. Retention is the
// only obligation packs carry today; the further ADR-0042 contributions
// (FiscalFormatter, ConformityRegime, …) return when a work package pays
// for them.
type Pack interface {
	// Code is the ISO 3166-1 alpha-2 code, lower-case ("de").
	Code() string

	// Retention returns the pack's statutory retention classes (GoBD in
	// the DE pack); nil when the pack adds none.
	Retention() Retention
}

// Retention exposes statutory retention classes the core retention engine
// consults; the engine stays core, the classes come from the pack.
type Retention interface {
	Classes() []RetentionClass
}

// RetentionClass names one statutory retention floor: the core engine
// treats Years as a minimum — a workspace policy may keep longer, never
// destroy earlier.
type RetentionClass struct {
	Name  string
	Years int
}
