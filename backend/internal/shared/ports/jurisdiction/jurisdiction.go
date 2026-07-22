// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package jurisdiction is the Tier-0 seam behind country packs
// (architecture/14, ADR-0042): country-specific behavior lives in a
// self-contained pack composed in at compile time — today a module
// registering in init(), migrating to the extension tier's Registry
// (ADR-0069). Core code never imports a pack and never contains a
// jurisdiction string. The pack CONTRACT (Pack, Retention,
// RetentionClass) lives on the published surface
// backend/pkg/extension/jurisdiction so extensions can implement it;
// this package keeps the core-internal registry and re-exports the
// types as aliases so core call sites stay put.
//
// Deliberately absent: locale/i18n. Locale is per-user and orthogonal to
// jurisdiction (A57) — there is no LocaleBundle on this seam.
package jurisdiction

import (
	"fmt"
	"sort"
	"sync"

	pub "github.com/gradionhq/margince/backend/pkg/extension/jurisdiction"
)

// Pack is the published pack contract
// (backend/pkg/extension/jurisdiction), aliased so a pack registered by
// an extension and one registered by a core module are the same type.
type Pack = pub.Pack

// Retention is the published retention contract, aliased like Pack.
type Retention = pub.Retention

// RetentionClass is the published retention-class shape, aliased like Pack.
type RetentionClass = pub.RetentionClass

// Code is the published jurisdiction-code type, aliased like Pack.
type Code = pub.Code

// Period is the published calendar-period type, aliased like Pack.
type Period = pub.Period

// RetentionClassName is the published closed class vocabulary, aliased
// like Pack; the named classes ride along so core engines consult the
// same constants extensions declare.
type RetentionClassName = pub.RetentionClassName

const (
	// CommercialCorrespondence is the published constant, re-exported.
	CommercialCorrespondence = pub.CommercialCorrespondence
	// AccountingRecords is the published constant, re-exported.
	AccountingRecords = pub.AccountingRecords
)

// Anchor is the published retention-anchor type, aliased like Pack.
type Anchor = pub.Anchor

const (
	// AnchorOccurrence is the published constant, re-exported.
	AnchorOccurrence = pub.AnchorOccurrence
	// AnchorCalendarYearEnd is the published constant, re-exported.
	AnchorCalendarYearEnd = pub.AnchorCalendarYearEnd
)

var (
	mu    sync.RWMutex
	packs = map[Code]Pack{}
)

// Register is called from a pack's init(); a duplicate or invalid code
// (Code.Validate) is a wiring defect and fails fast at boot.
func Register(p Pack) {
	mu.Lock()
	defer mu.Unlock()
	code := p.Code()
	if err := code.Validate(); err != nil {
		panic(fmt.Sprintf("jurisdiction: %v", err))
	}
	if _, dup := packs[code]; dup {
		panic(fmt.Sprintf("jurisdiction: pack %q registered twice", code))
	}
	packs[code] = p
}

// For returns the pack for a code; ok is false when the running binary
// was not compiled with it.
//
//nolint:ireturn // Pack IS the seam: packs are interface implementations behind one registry; returning the interface is the design.
func For(code Code) (Pack, bool) {
	mu.RLock()
	defer mu.RUnlock()
	p, ok := packs[code]
	return p, ok
}

// Applicable returns every compiled-in pack, sorted by code — the
// core-composed set future EU-wide behavior iterates (no pack ever
// imports another pack).
func Applicable() []Pack {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]Pack, 0, len(packs))
	for _, p := range packs {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code() < out[j].Code() })
	return out
}
