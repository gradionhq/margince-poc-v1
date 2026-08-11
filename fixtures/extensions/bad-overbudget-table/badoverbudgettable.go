// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package badoverbudgettable is a NEGATIVE fixture: a unit whose migration
// declares a correctly namespaced table whose DERIVED identifier is 64 bytes.
// It must never compose.
//
// This is the failure the budget check exists for, and it is worse than an
// error: PostgreSQL truncates an identifier past 63 bytes SILENTLY — no
// warning, no error — so the table would be created under a shortened name, the
// migration would report success, and two long names agreeing in their first 63
// bytes would become one object. The refusal has to name the line, because the
// identifier the author has to shorten is derived and appears nowhere in the
// file as written.
//
// Copy it under extensions/ and run `make composition` to reproduce the refusal
// by hand; nothing composes it otherwise.
package badoverbudgettable

import "github.com/gradionhq/margince/backend/pkg/extension"

// New returns the declaration. Well-formed on purpose — the fixture must fail
// on its SQL and nothing else.
func New() extension.Extension {
	return extension.Extension{
		Name:    "bad-overbudget-table",
		Version: "1.0.0",
	}
}
