// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package dealrooms owns the buyer-facing Deal Room: one room per deal, the
// immutable releases that fix what a buyer was shown, the named people invited
// into it, the credentials that admit them, and the shared to-do list both
// sides work from.
//
// THE ROOM IS A PROJECTION, NOT A SECOND CRM. Nothing here duplicates a record
// the deal already owns. The room points at a deal; a published release freezes
// the exact editorial text a buyer saw, so answering "what did they actually
// see in August?" never depends on what the deal says today. That is what makes
// a public edge safe to serve at all: it reads a release, never the live deal.
//
// WHAT IS BUILT TODAY is the seller's half: the room, its lifecycle, its
// releases, and the people admitted to it. A room reads and writes through the
// SELLER's authority, which it takes from the parent deal — deal_room carries no
// owner of its own, so every read joins deal and applies that row-scope clause,
// and every write takes auth.EnsureWritable on the same deal on top.
//
// A BUYER IS NOT A SEAT. A participant is no app_user, consumes no licence and
// holds no CRM authority. A steward admits one, corrects them, reissues their
// credential and takes access away; all four are human-only, because deciding
// which outsider reads a deal's material is not a judgement an agent makes.
//
// WHAT IS NOT BUILT is the buyer's own half: the credential EXCHANGE and the
// room-scoped session it produces. deal_room_session has a table and no Go code.
// When that lands, the session must be resolved fresh on every request, because
// a cached one would keep answering after the seller withdrew access.
//
// That slice carries one constraint worth stating before it is written, since
// getting it wrong is not recoverable by review: platform/auth's object and
// row-scope helpers admit a system principal UNCONDITIONALLY, so a buyer request
// leaning on them would hold the run of the workspace. Its authority has to come
// from the session, through store methods carrying a mandatory room predicate,
// with a fitness test over the whole public-reachable call graph — not a comment
// like this one.
//
// Tables owned: deal_room, deal_room_release, deal_room_participant,
// deal_room_invitation, deal_room_session, deal_room_task.
package dealrooms
