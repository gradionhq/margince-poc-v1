// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package personbrief writes the standing relationship brief on the person
// record page.
//
// Tables owned: person_brief (the per-user brief cache).
//
// It is the company brief applied to a person, and the mirroring is the ruling
// rather than a coincidence (ADR-0097 D4): reuse over reinvention, so the two
// briefs cannot drift into two different sets of guarantees. Four rules carry
// over unchanged, and each one exists because its absence was a real failure.
//
// PER VIEWER. The input is assembled by running the person's own reads AS THE
// REQUESTING CALLER, inside the normal gates. Not a filter applied afterwards:
// the scope is inherited from the caller's own gated read, so the brief can
// only describe records that caller could open themselves. A single shared
// brief would either leak scoped deals and activities to a restricted reader,
// or degrade to the lowest common scope and tell the record owner less than
// they can already see.
//
// CACHED ON THE INPUTS, not on the record. The key is a fingerprint over the
// assembled input plus the prompt and model-routing versions. Activities,
// claims and deals move without touching the person row, so a key derived from
// that row would serve a brief describing a relationship that has since
// changed, indefinitely. A stale-fingerprint brief is rewritten BEFORE the
// request answers, so a brief that arrives is current.
//
// DEGRADES RATHER THAN FAILS. With no model lane configured or the workspace's
// AI budget spent, the brief is a deterministic composition occupying the same
// component — never a blank AI card — and generated_by names which wrote it.
//
// EVERY SENTENCE IS CITED OR DROPPED. A sentence whose citations do not resolve
// is dropped whole rather than shown uncited: a readable claim standing on
// partial evidence is the one thing the grounding rule exists to prevent.
//
// There is no free-text ask on a person (ADR-0096 D6). Prepared, citable
// questions may come later; a chat box is not the product.
package personbrief
