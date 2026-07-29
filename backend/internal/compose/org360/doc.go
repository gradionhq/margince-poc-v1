// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package org360 assembles the company record page in one read: profile,
// contacts with their relationship strength, deals, timeline, tags and
// list memberships, decidable approvals, open next steps, and what has
// changed since the caller last acknowledged seeing the account.
//
// It lives in compose because it spans organization, person, relationship,
// deal, activity, tag, list and approval — the composition layer's charter
// — and it durably owns exactly one table of its own:
//
//	Tables owned: user_record_view (the per-user visit baseline).
//
// Two rules shape everything here.
//
// One transaction, one instant. Every section reads inside a single
// database.WithWorkspaceTx and the response carries the as_of stamp of
// that read. The isolation level is Read Committed (the platform's
// posture), so a concurrent commit can land between two sections; the
// stamp is what keeps that honest instead of hidden. Nothing here opens a
// second transaction, which is why the module stores it calls all expose
// transaction-taking variants.
//
// Authorization per section. Reading the organization is mandatory.
// Everything else needs its own object grant, and a section the caller may
// not read is OMITTED and named in sections_omitted — never returned
// empty, because "you may not see this" and "there is none" are different
// answers and a UI that conflates them lies to the rep.
package org360
