// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package extension is the published declaration surface of the stable
// extension tier (ADR-0069): one named, versioned, compile-time unit
// under root extensions/<name>/ that lands without editing any
// upstream-owned file. An extension exports `func New() extension.Extension`
// returning its declaration as a plain value; the generated composition
// (build/composition/, emitted by tools/gen-composition) collects every
// enabled unit's value and the process roles reconcile the set into the
// core registries at boot — the ONE registration idiom (EXT-P4).
//
// A declaration is inert data: it holds no handle into the core and
// extensions share no memory through it — each New() builds its own
// value, and only the boot reconciliation (after the whole set
// validated) applies anything. Capabilities are fields; a new capability
// kind is a new field, so existing declarations and extension test
// suites keep compiling (EXT-P3: grow additively, never in place). New
// gains a Deps parameter through a versioned successor when the first
// capability needs injected dependencies.
package extension

import "github.com/gradionhq/margince/backend/pkg/extension/jurisdiction"

// Extension is one installed unit's declaration.
type Extension struct {
	// Name is the canonical extension name (the extensions/<name>
	// directory name): lower-case, [a-z0-9-], stable across versions.
	// It keys the namespace at every layer (x_<name>_ tables,
	// /x/<name>/ paths, the x_<name> database role).
	Name string

	// Version is the extension's own version string, reported in the
	// boot inventory; it carries no authority (ADR-0069 §7 binds
	// operator decisions to digests, never to a version string).
	Version string

	// Jurisdictions are the unit's jurisdiction packs (policy suppliers
	// to the core retention engine — never actors). A duplicate
	// jurisdiction code across the composed set is a wiring defect and
	// fails the boot.
	Jurisdictions []jurisdiction.Pack
}
