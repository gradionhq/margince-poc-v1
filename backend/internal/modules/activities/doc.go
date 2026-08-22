// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package activities owns the activity timeline — logging (with
// source-system idempotency), reading and listing activities and their
// polymorphic links to person/organization/deal records — as store +
// contract mapping + transport handlers + the activities slice of the
// datasource provider, flat per ADR-0054 §3.
//
// Tables owned: activity, activity_link, activity_audience_member,
// activity_retention_evidence, transcript_read, attachment_extraction,
// deal_document_hide.
//
// transcript_read is the run record for reading a meeting transcript for
// the next steps in it (S-E04.3): the POST answers 202 with its id and the
// client polls it, because a model call cannot happen inside the request
// that asks for it. It records what one reading did, never what may be
// done about it — the proposals it stages are approval rows and the
// authority lives there (ADR-0036).
//
// attachment_extraction is the same shape for the same reason, over an
// attached document (RD-DDL-4). It differs in one way that matters: it
// STORES what the reading grounded, because a document reading stages no
// approval row and the accept is a human's own direct call. Those stored
// fields are what an accept resolves its value from, so a later reading of
// the same document cannot change what a human already agreed to
// (RD-AC-N-5).
//
// Activities have no owner_id; their visibility walks the linked
// records' row scope via platform/auth.ActivityContentClause — the scope
// rule lives in the platform (one spelling, ADR-0054 §8) because
// people's promotion-evidence check enforces the same clause. Single-row
// access carries that clause inside readActivity, so get, update, archive
// and relink alike answer an out-of-scope id with ErrNotFound and no call
// site can reach a row by forgetting to probe. Imports
// shared + platform + the generated contract only; never a sibling
// module. Every write rides storekit's audit+outbox shape and every
// entry point is gated by platform/auth.
package activities
