// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package accountdraft writes the first message of a conversation that does
// not exist yet: an email to an account, grounded in that account's records
// (ADR-0087/A132).
//
// It is the drafting half of the account-started pair. `POST /emails` sends a
// new conversation from a company with no anchor activity; the reply-side
// drafter (modules/activities) answers a message and needs one. An
// account-started draft has neither, and the product refuses to fabricate a
// placeholder activity to get one — so this is its own lane rather than a
// special case inside that one.
//
// Three rules shape every file here.
//
// **It writes nothing.** Not a record field, not an activity, not a
// voice-learning signal. The reply drafter records a served draft so the voice
// model can learn from what the rep changed; this one does not, and
// `draft_ref` is null for exactly that reason. There is no transaction in this
// package and no store with a write method injected into it — the guarantee is
// structural rather than a rule someone has to remember. Sending stays `POST
// /emails`, with its consent gate, approval token and idempotency key.
//
// **It is written per viewer, from the caller's own 360.** The composite read
// runs inside the normal gates, so a draft can only mention records the caller
// could open themselves. A rep who cannot see a deal gets a draft that does
// not know about it, rather than one that mentions it.
//
// **It degrades rather than failing.** With no model lane configured, or the
// workspace's budget spent, the deterministic floor writes the draft and
// `generated_by` says so. A rep who pressed "Write email" gets a message to
// edit either way.
//
// Everything but the caller's own intent is untrusted text — a contact's name,
// a deal's name, a subject line someone else chose — and is fenced. The intent
// is the one input the caller typed themselves, and it is the one thing
// outside the fence.
package accountdraft
