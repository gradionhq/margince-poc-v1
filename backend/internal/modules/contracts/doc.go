// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package contracts owns the agreements an account has signed: their terms,
// their renewal dates, their cancellation, and the renewal chain that links a
// succession of them (ADR-0109/A160).
//
// Tables owned: contract.
//
// Spine: Handlers→Store. A contract is a record, not an engine — the one
// multi-step path (renewal, which creates a successor and supersedes its
// predecessor) is a single transaction inside the store rather than a service,
// because there is no domain logic between the two writes to put anywhere else.
//
// TWO THINGS THIS MODULE WILL NOT DO.
//
// It does not infer a status. Every transition is asserted by a human or by an
// approved proposal; no date moves a contract from active to expired, here or
// anywhere. The data that would drive an inference — a term end that passed
// last month — is exactly the data most likely to be stale, and a term everybody
// knows was extended by email would otherwise read as a lapsed agreement.
//
// It does not reconcile against invoices. A contract's value is what a human
// asserted was agreed; the finance mirror holds what was actually invoiced and
// owns its own authority (ADR-0083/A128). Comparing the two is a real question
// and it is not this module's to answer.
//
// The table carries no workspace column (ADR-0091/A136 — this is the first
// table authored after the tenant boundary was retired) and no owner column:
// visibility is derived from the linked deal, falling back to the organization,
// so reassigning a deal moves its contracts in the same query.
package contracts
