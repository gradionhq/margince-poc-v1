// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package auth

import "github.com/gradionhq/margince/backend/internal/shared/kernel/principal"

// The read classes of the row-scoped business records. Row scope (own / team /
// all) is a property of the PRINCIPAL; which tables it narrows is a property
// of the TABLE, and that classification lives here so the two cannot drift.
//
// Customer identity and the work done for it are shared across the workspace.
// A person, a company, a lead, a deal and a project are readable by every seat
// that holds the object grant, whatever its row scope, because the alternative
// is the failure this model exists to end: a rep who cannot see that a company
// is already a customer of another team contacts it again. Row scope and
// record grants keep governing WRITES to these tables (writescope.go), and
// capture privacy — a row a connector minted as `visibility='owner'` — still
// narrows person and organization for everyone but its owner until it is
// promoted.
//
// A project reads like the rest of them. It used to keep the own/team/all
// predicate on the reasoning that commercial work is scoped, and that was the
// wrong reading of the most collaborative record in the product: a consultant
// delivering a project they neither own nor were granted got a 404 on the
// record they were working. What keeps a project the owner's to change is the
// write arm, not the read arm.

// identityTables are read by every seat of the workspace: the own/team owner
// predicate renders TRUE for them and only the capture-privacy and grant arms
// (person, organization) remain.
//
// Every shareable table is in this set today. The set stays spelled out rather
// than collapsed into shareableTables because the two answer different
// questions — which rows a seat READS, versus which rows a manual grant can
// widen — and a record type that arrives scoped will join one without the
// other.
var identityTables = map[string]bool{
	tablePerson: true, tableOrganization: true, tableLead: true, tableDeal: true, tableProject: true,
}

// readsEveryRow reports whether the principal's READ of the table carries no
// owner-scope arm: an unbounded actor, or any actor on an identity table. It is
// the read-side twin of Unbounded and deliberately says nothing about writes.
//
// The identity-table arm says "no owner predicate narrows this read", which is
// true for a seated actor and false for a buyer: a Deal Room participant's
// reads are bounded by their room, and no identity table carries that bound. So
// the kind is answered before the table is, or a buyer would read every person,
// organization, lead, deal and project in the installation.
func readsEveryRow(p principal.Principal, table string) bool {
	if p.Type == principal.PrincipalBuyer {
		return false
	}
	return Unbounded(p) || identityTables[table]
}
