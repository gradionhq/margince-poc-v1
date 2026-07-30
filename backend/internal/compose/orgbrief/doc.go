// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package orgbrief writes the standing account brief on the company view:
// what this account is, where it stands, and what changed.
//
//	Tables owned: org_brief (the per-user brief cache).
//
// Three rules shape it.
//
// PER VIEWER, because visibility is per viewer. The input is assembled by
// running the account's reads AS THE REQUESTING CALLER, inside the normal
// gates, so a brief can only describe records that caller could open
// themselves. One shared brief would either leak scoped deals and
// activities to a restricted reader, or degrade to the lowest common scope
// and tell the account owner less than the page already shows them.
//
// CACHED ON THE INPUTS, not on the record. The key is a fingerprint over
// the assembled input plus the prompt, task and routing versions. Facts,
// deals and activities all move without touching the organization row, so a
// key derived from that row would serve a brief describing a pipeline the
// account no longer has, indefinitely. A cached brief whose fingerprint no
// longer matches is rewritten before the request answers, so a brief that
// arrives is current.
//
// DEGRADES RATHER THAN FAILS. With no model lane configured, or the
// workspace's AI budget exhausted, the brief falls back to a deterministic
// structured summary over the same inputs. generated_by names which wrote
// it, because a reader deciding how much to trust a sentence needs to know.
//
// The prompt treats every activity subject and body as untrusted quoted
// data, the same discipline the site-read prompts use: this text arrives
// from outside the workspace and must never be read as instruction.
package orgbrief
