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

import (
	"fmt"
	"strings"
	"time"
)

// Code is a lower-case ISO 3166-1 alpha-2 jurisdiction code ("de").
type Code string

// Validate enforces the alpha-2 grammar. The registry refuses an invalid
// code at boot: For could never resolve it canonically, so a malformed
// code would be a pack that looks registered and never applies.
func (c Code) Validate() error {
	if len(c) != 2 || c[0] < 'a' || c[0] > 'z' || c[1] < 'a' || c[1] > 'z' {
		return fmt.Errorf("jurisdiction code %q is not a lower-case ISO 3166-1 alpha-2 code", string(c))
	}
	return nil
}

// Period is a calendar period — the date part of an ISO 8601 duration
// (PnYnMnD). Statutory retention floors are calendar spans: six YEARS is
// not 2190 days, and a day-count conversion silently shortens the floor
// across leap years — destructive retention must never run early.
type Period struct {
	Years  int
	Months int
	Days   int
}

// String renders the ISO 8601 date interval ("P6Y", "P1Y6M", "P0D") —
// also the literal form Postgres accepts as an interval, so the same
// bytes drive calendar arithmetic on both sides of the seam.
func (p Period) String() string {
	var b strings.Builder
	b.WriteString("P")
	if p.Years != 0 {
		fmt.Fprintf(&b, "%dY", p.Years)
	}
	if p.Months != 0 {
		fmt.Fprintf(&b, "%dM", p.Months)
	}
	if p.Days != 0 || b.Len() == 1 {
		fmt.Fprintf(&b, "%dD", p.Days)
	}
	return b.String()
}

// Cutoff anchors the period at ref and returns the calendar point it
// reaches back to — the boundary a retention comparison uses (an earlier
// cutoff is the stricter floor).
func (p Period) Cutoff(ref time.Time) time.Time {
	return ref.AddDate(-p.Years, -p.Months, -p.Days)
}

// Pack is one jurisdiction's compiled-in behavior set. Retention is the
// only obligation packs carry today; the further ADR-0042 contributions
// (FiscalFormatter, ConformityRegime, …) return when a work package pays
// for them.
type Pack interface {
	// Code is the pack's jurisdiction, lower-case ISO 3166-1 alpha-2.
	Code() Code

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
// treats Keep as a minimum — a workspace policy may keep longer, never
// destroy earlier.
type RetentionClass struct {
	Name string
	Keep Period
}
