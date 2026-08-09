// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package orgdossier assembles what a company IS, from the company's own
// facts, with every sentence citing one (company-dossier.md).
//
// It is the sibling of the account brief, not its child. The brief answers
// what is happening with this relationship; the dossier answers what is this
// company. They are deliberately not merged — a page that mixes "they operate
// in Germany and Austria" with "the economic buyer has not replied in eighteen
// days" gives a reader no way to know which claims age in weeks and which in
// hours.
//
// THE INPUTS ARE THE FACTUAL SIDECARS ONLY (DOSS-AC-4): profile fields,
// extracted facts, and the inventory of sources that were read. Not the
// assembled account composite. A dossier that could see the pipeline would
// describe the pipeline, and the separation from the brief would collapse on
// the first prompt revision.
//
// THE GROUNDING FILTER IS NOT SPELLED HERE. It lives once, in
// internal/compose/claims, shared with the brief — three copies of a grounding
// rule is three chances for one of them to be lenient. What this package owns
// is the set of records it actually supplied, which is the half only it can
// know.
//
// THE CACHE IS KEYED PER READER, and that is a guarantee rather than a
// performance choice. A claim's evidence labels name files and activities;
// those records are row-scoped per reader; a shared assembly would disclose
// that such a record EXISTS to a reader who cannot see it (DOSS-AC-11). No
// stable signature summarizes a reader's scope over an open-ended cited set,
// so the reader is the key.
package orgdossier
