// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package jurisdiction is the Tier-0 seam behind country packs
// (architecture/14, ADR-0042): country-specific behavior lives in a
// self-contained pack module (crm-de, …) composed in at compile time by
// the edge binary's require-set. Core code never imports a pack and never
// contains a jurisdiction string; packs register here in init().
//
// Deliberately absent: locale/i18n. Locale is per-user and orthogonal to
// jurisdiction (A57) — there is no LocaleBundle on this seam.
package jurisdiction

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// Pack is one jurisdiction's compiled-in behavior set.
type Pack interface {
	// Code is the ISO 3166-1 alpha-2 code, lower-case ("de").
	Code() string

	// Fiscal returns the pack's fiscal document behavior (e-invoice
	// formats, export profiles). Nil-safe: a pack without fiscal behavior
	// returns nil.
	Fiscal() Fiscal

	// Retention returns the pack's statutory retention classes (GoBD in
	// the DE pack); nil when the pack adds none.
	Retention() Retention

	// Conformity returns the pack's regulatory conformity regime (the CRA
	// DoC regime moved pack-side per A57); nil when none.
	Conformity() Conformity
}

// Fiscal renders and validates jurisdiction-specific fiscal documents.
type Fiscal interface {
	Formats() []string // e.g. "xrechnung", "zugferd"
	Render(ctx context.Context, format string, doc []byte) ([]byte, error)
}

// Retention exposes statutory retention classes the core retention engine
// consults; the engine stays core, the classes come from the pack.
type Retention interface {
	Classes() []RetentionClass
}

type RetentionClass struct {
	Name  string
	Years int
}

// Conformity produces the pack's conformity artifacts (e.g. the CRA
// Declaration of Conformity regime).
type Conformity interface {
	Regime() string
	Artifacts(ctx context.Context) ([][]byte, error)
}

var (
	mu    sync.RWMutex
	packs = map[string]Pack{}
)

// Register is called from a pack's init(); a duplicate code is a wiring
// defect and fails fast at boot.
func Register(p Pack) {
	mu.Lock()
	defer mu.Unlock()
	code := p.Code()
	if _, dup := packs[code]; dup {
		panic(fmt.Sprintf("jurisdiction: pack %q registered twice", code))
	}
	packs[code] = p
}

// For returns the pack for a code; ok is false when the running binary
// was not compiled with it.
func For(code string) (Pack, bool) {
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
