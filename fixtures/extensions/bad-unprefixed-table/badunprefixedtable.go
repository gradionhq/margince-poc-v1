// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package badunprefixedtable is a NEGATIVE fixture: a unit whose migration
// creates `ext.notes_note` — the table notes owns, spelled WITHOUT the
// unit namespace. It must never compose.
//
// Namespacing is the migrations layer's whole claim, and it is a claim about a
// SHARED schema: every installed unit's tables live in ext, so an unprefixed
// name is one unit reaching into a name another unit could own, in the one
// place PostgreSQL will not object. A textual scanner catches it before any SQL
// runs, at the offending line, which is what makes the refusal actionable —
// see extmigrations_fixture_test.go.
//
// It is a fixture rather than a temp-dir string so a human can point the
// generator at it: `cp -R fixtures/extensions/bad-unprefixed-table extensions/
// && make composition` reproduces the refusal by hand. Nothing copies it in CI
// (the extension lane copies crm-hello only), so the vanilla composed set never
// sees it.
package badunprefixedtable

import "github.com/gradionhq/margince/backend/pkg/extension"

// New returns the declaration. It is well-formed on purpose: the fixture must
// fail on its SQL and on nothing else, or the test asserting the refusal would
// be provoking a different one.
func New() extension.Extension {
	return extension.Extension{
		Name:    "bad-unprefixed-table",
		Version: "1.0.0",
	}
}
