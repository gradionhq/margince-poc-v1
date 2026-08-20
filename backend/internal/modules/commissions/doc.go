// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package commissions owns what a partner earned on a won deal: the ledger of
// accruals, their approval and payment, and the reversals that undo them.
//
// Tables owned: commission_entry.
//
// Spine: Handlers→Store. An entry is a record with a small closed lifecycle
// (accrued → approved → paid, or void at any point), and each transition is one
// transaction inside the store; there is no multi-step domain logic between
// writes to put in a service.
//
// It is a LEDGER, not a calculation. Money that was owed and then was not is a
// second row — a reversal pointing at the original, which goes void — never an
// edited first one. Recomputing an entry when the deal behind it changes would
// silently rewrite what a partner was already told they had earned, and the
// dispute that follows has nothing to read.
//
// Every rate-bearing value is SNAPSHOT at accrual: the partner's margin tier,
// the deal's amount and currency, the won-time rate to base. A tier is config a
// human can change next quarter and a deal amount can be corrected after the
// close, so an entry that resolved them live would answer a different question
// every time it was read. An entry says what the arrangement WAS the day the
// deal was won.
//
// WHY IT IS ITS OWN MODULE. people owns the partner extension and deals owns
// the deal, but a module never imports a sibling and neither of those owns a
// financial ledger. finance is a READ-ONLY mirror of an accounting source whose
// no-write posture is the absence of a grant, so a writable ledger cannot live
// there either. The edge from a won deal to an accrual is injected in compose,
// where every cross-module edge belongs.
//
// The table carries no workspace column (the tenant boundary is retired) and no
// owner column: visibility is inherited from the deal the entry belongs to, so
// reassigning a deal moves its commission entries in the same query.
package commissions
