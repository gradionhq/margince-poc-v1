// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package extension is the published declaration surface of the stable
// extension tier: one named, versioned, compile-time unit
// under root extensions/<name>/ that lands without editing any
// upstream-owned file. An extension exports `func New() extension.Extension`
// returning its declaration as a plain value; the generated composition
// (build/composition/, emitted by tools/gen-composition) collects every
// enabled unit's value and the process roles reconcile the set into the
// core registries at boot — the ONE registration idiom.
//
// A declaration is inert data: it holds no handle into the core and
// extensions share no memory through it — each New() builds its own
// value, and only the boot reconciliation (after the whole set
// validated) applies anything. Capabilities are fields; a new capability
// kind is a new field, so existing declarations and extension test
// suites keep compiling (grow additively, never in place). New
// gains a Deps parameter through a versioned successor when the first
// capability needs injected dependencies.
//
//margince:extension-surface
package extension

import (
	"fmt"
	"io/fs"
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
// and two long names could collide on one `ext_<name>` role. 32 leaves 26
// bytes for a table suffix in `ext_<name>_<table>`; the suffix's own
// share is enforced where tables are DECLARED — the extension-migration
// slice validates every complete derived identifier
// against the full budget, since only the migration knows its table
// names. The name cap alone deliberately does NOT claim that guarantee.
const maxNameLength = 32

// Name is the canonical extension name and must equal the
// extensions/<name> directory name, stable across versions. It keys the
// namespace at every layer (ext_<name>_ tables, /v1/ext/<name>/ paths, the
// ext_<name> database role).
type Name string

// Validate enforces the exact grammar — lower-case [a-z0-9] segments
// joined by single hyphens, `^[a-z0-9]+(-[a-z0-9]+)*$`, at most 32
// characters — so no leading, trailing, or doubled hyphen, and nothing
// a 63-byte SQL identifier would truncate; anything else would leak
// into SQL identifiers and URL paths. Boot registration refuses the set
// on a violation.
func (n Name) Validate() error {
	if !nameGrammar.MatchString(string(n)) {
		return fmt.Errorf("extension name %q is not a valid unit name (lower-case [a-z0-9] segments joined by single hyphens)", string(n))
	}
	if len(n) > maxNameLength {
		return fmt.Errorf("extension name %q is %d characters — the unit name keys SQL identifiers (ext_<name>_<table>, 63-byte limit, 26 bytes left for the table suffix), so it is capped at %d", string(n), len(n), maxNameLength)
	}
	return nil
}

// NamespacePrefix is the one spelling of the extension namespace token. It
// opens every identifier a unit owns — `ext_<name>_<table>` tables, the
// `ext_<name>` database role, the `ext_<name>` migration namespace — so a
// core object can never be mistaken for an extension's and no unit can
// address another's. Changing it is a breaking rename of the whole tier.
const NamespacePrefix = "ext_"

// Namespace maps a unit name onto the SQL-identifier namespace it owns:
// `foo-1` → `ext_foo_1`. The name grammar admits hyphens because a name is
// also a URL path segment; a SQL identifier cannot hold one unquoted, so the
// hyphen becomes an underscore here and nowhere else.
//
// It validates first rather than trusting its caller: the result is
// interpolated into SQL identifiers (a migration tracking table, a role
// name), and Validate is the ONE rule saying which byte sequences may get
// there. This function adds no refusals of its own, because between them the
// grammar and the prefix already leave nothing an unquoted identifier could
// not hold:
//
//   - nameGrammar excludes upper case, dots, quotes, spaces and every other
//     byte outside [a-z0-9-], and the hyphen is the one it admits that this
//     function converts.
//   - nameGrammar does NOT exclude a leading digit — `1foo` is a legal unit
//     name. The prefix is what makes that safe: a derived namespace always
//     begins `ext_`, so its first byte is never a digit.
//   - The 32-byte cap keeps `schema_migrations_ext_<name>` (18 + 4 + 32 = 54)
//     inside PostgreSQL's 63-byte limit.
//
// The derived namespace is NOT by itself a promise that a complete
// `ext_<name>_<table>` identifier fits: the table suffix's own share of the
// budget is checked where tables are declared (see maxNameLength).
func (n Name) Namespace() (string, error) {
	if err := n.Validate(); err != nil {
		return "", err
	}
	return NamespacePrefix + strings.ReplaceAll(string(n), "-", "_"), nil
}

// Version is the extension's own version string, expected stable for an
// unchanged unit: the boot inventory records it and logs a change. It
// carries no authority (operator decisions bind to digests,
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

	// Tools are the governed agent tools the unit contributes: named
	// operations running at a requested risk tier. Their tiers
	// and scopes are REQUESTS recorded in the manifest for operator
	// resolution — see Tool. Unlike a jurisdiction pack (passive policy),
	// a tool is a governed capability and appears in manifest.generated.json.
	Tools []Tool

	// Secrets are the secret keys the unit declares it will use, by name and
	// scope. Like a Tool's tier these are REQUESTS an operator resolves, not
	// facts: declaring a key mints nothing and reads nothing, and the live
	// port arrives only through the Runtime the core builds per invocation.
	//
	// This does not contradict "a declaration is inert data […] holds no
	// handle into the core" above — a SecretsRequest IS inert data, a name
	// and a scope, which is exactly what lets the generated manifest tell an
	// operator which secrets a unit expects before it ever runs.
	Secrets []SecretsRequest

	// Migrations is the unit's SQL schema layer: a read-only filesystem
	// holding the MigrationsDir directory of NNNN_name.up.sql/.down.sql
	// pairs, which a unit supplies with `//go:embed migrations`. A unit
	// that owns no tables leaves it nil, and that is the common case.
	//
	// EMBEDDED, not read back from the source tree, because the process
	// that applies it is a bare binary: Dockerfile.api ships
	// /usr/local/bin/margince-migrate and no repository, so a
	// path-relative read would apply a unit's migrations in dev and CI —
	// where the checkout is right there — and silently none in
	// production, which is the one place nobody watches a migration
	// count. The declaration carrying its own bytes is what makes the
	// composed binary self-sufficient.
	//
	// Still inert data: an fs.FS is bytes to read, not a handle into the
	// core. Applying them is the migrate role's job (cmd/migrate), after
	// the composed set is known; declaring them mints nothing.
	Migrations fs.FS
}

// MigrationsDir is the one spelling of the subdirectory a unit's
// Migrations FS is rooted above — `extensions/<name>/migrations/`. The
// generator that validates the layer and the migrate role that applies it
// must name the same directory, or a unit could pass the gate on files
// that are never applied.
const MigrationsDir = "migrations"
