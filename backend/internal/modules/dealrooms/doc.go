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
// A BUYER IS NOT A SEAT. A participant is not an app_user, consumes no licence,
// and holds no CRM authority. Their reach is one room, established by exchanging
// a one-time emailed credential for a room-scoped session that is resolved fresh
// on every request — that read IS the revocation guarantee, because a cached
// session would keep answering after the seller withdrew access.
//
// The authority for a buyer request therefore comes from the SESSION, never from
// the actor: platform/auth's object and row-scope helpers admit a system
// principal unconditionally, so leaning on them here would hand an external
// visitor the run of the workspace. Public reads go through store methods that
// carry a mandatory room predicate of their own, and nothing on that path calls
// a general-purpose store.
//
// Tables owned: deal_room, deal_room_release, deal_room_participant,
// deal_room_invitation, deal_room_session, deal_room_task.
package dealrooms
