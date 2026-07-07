// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ids

// Ref is the untyped edge for polymorphic seams — activity links,
// list/tag membership, attachments, the audit/event envelopes — where
// one column carries any entity's id beside an entity_type
// discriminator. Typed code mints a Ref via ID[K].Ref(), so the Type
// half always comes from the closed kind vocabulary; a Ref built from
// client input is validated against that vocabulary at its seam before
// it travels.
type Ref struct {
	Type string
	ID   UUID
}
