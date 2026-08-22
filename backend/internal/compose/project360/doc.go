// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package project360 assembles the project record page in one read: the
// project, the company it is for, its phase history with the time spent in
// each phase, the deals rolled up to it, the people seated on it, its
// contracts and documents, the open commitments filed under it, its
// timeline, how well its correspondence is filed, and the header figures.
//
// It is the organization 360's sibling (compose/org360) and keeps its two
// rules. It lives in compose because it spans deals, people, contracts and
// activities — the composition layer's charter — and it owns no table.
//
// One transaction, one instant. Every section reads inside a single
// database.WithWorkspaceTx and the response carries the as_of stamp of
// that read. Every module store this package calls exposes a
// transaction-taking variant of its read; the custom-field catalogs are
// the one exception and are read above the transaction, on their own
// connection, because they describe the workspace's column set and not
// the project's rows.
//
// Authorization per section. Reading the project is mandatory and its
// refusal is the whole read's refusal. Everything else needs its own object
// grant, and a section the caller may not read is OMITTED and named in
// sections_omitted — never returned empty, because "you may not see this"
// and "there is none" are different answers.
//
// No SQL against a module-owned table is written here. The phase history,
// the deal totals and the filing coverage are reads the owning module
// stores gained for this page (deals.ListProjectPhaseHistoryTx,
// deals.ProjectDealTotalsTx, activities.ProjectActivityFactsTx).
package project360
