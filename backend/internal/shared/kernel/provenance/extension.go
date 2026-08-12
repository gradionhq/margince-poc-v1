// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package provenance

import "context"

// Extension is the attribution a core write carries when an extension unit
// made it: which unit, which version of it, and through which of its verbs.
//
// It is a SECOND dimension beside the actor, never a replacement for one. A
// unit's core write runs on the authority of the human who triggered it
// (a unit may never exceed its caller), so the audit row's actor stays that
// human and `ext:notes` in actor_id would break both the actor index's meaning
// and the on-behalf-of story. Who acted and what carried the action are
// different questions, and this answers the second.
//
// Every field is stamped by the CORE from the composed declaration and the
// invocation — never by the unit — which is what makes the answer worth
// trusting. A unit's own free-form content rides in the evidence entry's
// `detail` member instead.
type Extension struct {
	// Unit is the extension's name, as the composition declares it.
	Unit string
	// Version is the unit's declared version at the time of the write.
	Version string
	// Via names the surface the call arrived on — `tool/file_note`,
	// `route/POST /ext/notes/file` — so an audit reader can tell an agent's
	// call from a person's without a second lookup.
	Via string
}

// ExtensionEvidenceKey is the one audit-evidence member extension attribution
// lands under, and it is CORE-OWNED: nothing but the stamp below may write it.
//
// audit_log.evidence is a flat namespace that ~20 modules write bare keys into
// with no coordination (`captured_by`, `source_system`, `policy`), so a tier
// that spread across several of them would collide with a module sooner or
// later. One key, spelled once, keeps the whole tier to a single member of that
// namespace — and makes the promotion path to a column later a matter of
// backfilling from `evidence->'extension'->>'unit'`.
const ExtensionEvidenceKey = "extension"

type extensionContextKey struct{}

// WithExtension binds the attribution every core write made under this context
// carries. The composition layer calls it once per invocation, around the
// handler; nothing a unit can reach binds it, because a unit that could name
// its own attribution could name another unit's.
func WithExtension(ctx context.Context, ext Extension) context.Context {
	return context.WithValue(ctx, extensionContextKey{}, ext)
}

// ExtensionFrom reports the bound attribution. ok is false for an ordinary core
// write — a person or an agent calling the product's own surface — which is
// what makes "this row was written through an extension" a fact the absence of
// the key states as clearly as its presence.
func ExtensionFrom(ctx context.Context) (Extension, bool) {
	ext, ok := ctx.Value(extensionContextKey{}).(Extension)
	return ext, ok
}

// EvidenceEntry renders the attribution as the value its evidence member holds.
//
//craft:ignore naked-any the audit evidence seam is jsonb; this is one member of it
func (e Extension) EvidenceEntry() map[string]any {
	return map[string]any{"unit": e.Unit, "version": e.Version, "via": e.Via}
}
