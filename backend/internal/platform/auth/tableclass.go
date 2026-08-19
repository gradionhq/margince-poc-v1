// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package auth

import "github.com/gradionhq/margince/backend/internal/shared/kernel/principal"

// The read classes of the row-scoped business records. Row scope (own / team /
// all) is a property of the PRINCIPAL; which tables it narrows is a property
// of the TABLE, and that classification lives here so the two cannot drift.
//
// Customer identity is shared across the workspace. A person, a company, a
// lead and a deal are readable by every seat that holds the object grant,
// whatever its row scope, because the alternative is the failure this model
// exists to end: a rep who cannot see that a company is already a customer of
// another team contacts it again. Row scope and record grants keep governing
// WRITES to these tables (writescope.go), and capture privacy — a row a
// connector minted as `visibility='owner'` — still narrows person and
// organization for everyone but its owner until it is promoted.
//
// Commercial work stays scoped: a project is visible by own / team / all plus
// record grants, exactly as before, because a project carries its own
// visibility semantics and was not part of the shared-identity decision.

// identityTables are read by every seat of the workspace: the own/team owner
// predicate renders TRUE for them and only the capture-privacy and grant arms
// (person, organization) remain.
var identityTables = map[string]bool{
	tablePerson: true, tableOrganization: true, tableLead: true, tableDeal: true,
}

// commercialTables keep the own/team/all read predicate.
var commercialTables = map[string]bool{tableProject: true}

// readsEveryRow reports whether the principal's READ of the table carries no
// owner-scope arm: an unbounded actor, or any actor on an identity table. It is
// the read-side twin of Unbounded and deliberately says nothing about writes.
func readsEveryRow(p principal.Principal, table string) bool {
	return Unbounded(p) || identityTables[table]
}
