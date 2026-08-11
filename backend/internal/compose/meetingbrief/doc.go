// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package meetingbrief assembles the pre-meeting brief for one booked meeting
// (ADR-0097 D5).
//
// Tables owned: none. Nothing here is written; every read runs under the
// caller's own scope through the same gated reads the person page serves.
//
// NO CACHE, and that is the deliberate difference from personbrief beside it.
// The person brief is a standing summary of a relationship, so caching it on a
// fingerprint of its inputs costs nothing a reader notices. This one is opened
// in the minutes before somebody walks into a room, and the whole value of it
// is that it knows what happened this morning: a commitment logged an hour ago,
// an attendee added to the invite, a stage moved yesterday. A cached artifact
// presented as current is the exact failure the spec names, so there is no
// cache table, no fingerprint, and no refresh route to regenerate a stale one —
// `generated_at` is always the instant of the read.
//
// The cost of that is one composite read per open, which is the cost of the
// person page itself and is paid for by a brief nobody has to distrust.
//
// EVERY SENTENCE IS CITED OR DROPPED. A sentence whose citations do not resolve
// to records the caller can open is dropped whole rather than shown uncited —
// the same rule the account and person briefs run, spelled once in
// internal/compose/claims. Dropping only the bad citation would leave a
// readable claim standing on partial evidence, which is the one thing the
// grounding rule exists to prevent.
//
// A SECTION WITH NOTHING TO SAY IS ABSENT. The spec says it of risks; it reads
// honestly for all eight, so an empty section is never emitted. A heading that
// turns out to hold nothing costs the reader the same glance as a heading that
// holds something, and the brief is specified as a two-to-three-minute read.
package meetingbrief
