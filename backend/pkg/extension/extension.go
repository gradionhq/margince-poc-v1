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
//
//margince:extension-surface
package extension

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/gradionhq/margince/backend/pkg/extension/jurisdiction"
)

// nameGrammar is the one spelling of the unit-name rule; the grammar in
// prose lives on Name. The generator (tools/gen-composition) validates
// through this same method, so scan-time acceptance can never drift from
// boot-time validation.
var nameGrammar = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// maxNameLength bounds the unit name's SHARE of PostgreSQL's 63-byte
// identifier budget — a longer name would be silently TRUNCATED there,
// and two long names could collide on one `x_<name>` role. 32 leaves 28
// bytes for a table suffix in `x_<name>_<table>`; the suffix's own
// share is enforced where tables are DECLARED — the extension-migration
// slice (ADR-0069 §9) validates every complete derived identifier
// against the full budget, since only the migration knows its table
// names. The name cap alone deliberately does NOT claim that guarantee.
const maxNameLength = 32

// Name is the canonical extension name and must equal the
// extensions/<name> directory name, stable across versions. It keys the
// namespace at every layer (x_<name>_ tables, /x/<name>/ paths, the
// x_<name> database role).
type Name string

// Validate enforces the exact grammar — lower-case [a-z0-9] segments
// joined by single hyphens, `^[a-z0-9]+(-[a-z0-9]+)*$`, at most 32
// characters — so no leading, trailing, or doubled hyphen, and nothing
// a 63-byte SQL identifier would truncate; anything else would leak
// into SQL identifiers and URL paths. Boot registration refuses the set
// on a violation.
func (n Name) Validate() error {
	if !nameGrammar.MatchString(string(n)) {
		return fmt.Errorf("extension name %q is not a valid unit name (lower-case [a-z0-9] segments joined by single hyphens, ADR-0069 §2)", string(n))
	}
	if len(n) > maxNameLength {
		return fmt.Errorf("extension name %q is %d characters — the unit name keys SQL identifiers (x_<name>_<table>, 63-byte limit), so it is capped at %d", string(n), len(n), maxNameLength)
	}
	return nil
}

// Version is the extension's own version string, expected stable for an
// unchanged unit: the boot inventory records it and logs a change. It
// carries no authority (ADR-0069 §7 binds operator decisions to digests,
// never to a version string).
type Version string

// Validate requires a non-empty, single-line printable string — the
// inventory writes it into system_log verbatim, so control characters
// and whitespace framing have no honest reading there.
func (v Version) Validate() error {
	if v == "" {
		return fmt.Errorf("extension version is empty — the boot inventory records it")
	}
	if strings.TrimSpace(string(v)) != string(v) {
		return fmt.Errorf("extension version %q carries surrounding whitespace", string(v))
	}
	for _, r := range v {
		if !unicode.IsPrint(r) {
			return fmt.Errorf("extension version %q carries a non-printable character", string(v))
		}
	}
	return nil
}

// Extension is one installed unit's declaration.
type Extension struct {
	Name    Name
	Version Version

	// Jurisdictions are the unit's jurisdiction packs (policy suppliers
	// to the core retention engine — never actors). A duplicate
	// jurisdiction code across the composed set is a wiring defect and
	// fails the boot.
	Jurisdictions []jurisdiction.Pack
}
