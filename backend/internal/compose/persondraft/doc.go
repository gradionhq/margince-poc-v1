// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package persondraft writes an email to one contact, grounded in that
// contact's own record.
//
// It is the person-side mirror of accountdraft. The account drafter has to be
// told which contact it is writing to, because an account has many; here the
// record IS the recipient, so the request carries nothing but the caller's
// optional steering. What the draft stands on is what the person page stands
// on: who they are and where they work, the open deal and the money on it, the
// claims they have made, and the recent conversation.
//
// Three rules shape every file here.
//
// **It changes no record.** Not a field on the person, not an activity, not a
// voice-learning signal. There is no pool in this package and no store with a
// write method injected into it — the guarantee is structural rather than a
// rule someone has to remember. Sending stays `POST /emails`, with its consent
// gate, approval token and idempotency key.
//
// Two writes DO happen further down, and both are about the call rather than
// the person: the model router meters the workspace's AI usage and records the
// call for audit, as it does for every model-backed read on this page.
//
// **It is written per viewer, from the caller's own person 360.** The composite
// read runs inside the normal gates, so a draft can only mention records the
// caller could open themselves. A rep with no deal grant gets a draft that does
// not know about the deal, rather than one that mentions it.
//
// **It degrades rather than failing.** With no model lane configured, or the
// workspace's budget spent, the deterministic floor writes the draft and
// `generated_by` says so. A rep who pressed "Write email" gets a message to
// edit either way.
//
// Everything but the caller's own intent is untrusted text — the contact's
// name, a deal's name, the body of a claim read out of somebody else's email —
// and is fenced. The intent is the one input the caller typed themselves, and
// it is the one thing outside the fence.
package persondraft
