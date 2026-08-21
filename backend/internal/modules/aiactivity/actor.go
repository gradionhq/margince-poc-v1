// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package aiactivity

import (
	"fmt"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// ResolveActor derives who an occurrence belongs to FROM THE ENVELOPE, never
// from the payload. An emitter chooses its payload; it cannot choose the
// authenticated actor the write shape stamped, so it cannot attribute its work
// to somebody else by filling in a field.
//
// Stated honestly: on a worker path OnBehalfOf is itself derived from the job's
// own args, so this is uniform rather than tamper-proof. Uniform is the benefit
// worth having — one rule, one place, one failure mode.
//
// A non-human actor with nobody behind it is workspace-scoped: it belongs to
// nobody by nature. A HUMAN one that does not parse is refused instead, because
// quietly making it workspace-scoped is how one person's work becomes a system
// sweep nobody can find and nobody notices is missing.
func ResolveActor(a events.Actor) (scope string, user ids.UUID, err error) {
	if a.Type == "human" {
		if a.OnBehalfOf != nil && !a.OnBehalfOf.IsZero() {
			return "", ids.Nil, fmt.Errorf("aiactivity: human actor %q also names on_behalf_of %s — no writer produces that, and guessing which half owns the work files it under the wrong person", a.ID, a.OnBehalfOf)
		}
		parsed, ok := principal.HumanUserID(a.ID)
		if !ok {
			return "", ids.Nil, fmt.Errorf("aiactivity: human actor id %q is not %q<uuid>", a.ID, principal.HumanIDPrefix)
		}
		return ScopePersonal, parsed, nil
	}
	if a.OnBehalfOf != nil && !a.OnBehalfOf.IsZero() {
		return ScopePersonal, *a.OnBehalfOf, nil
	}
	return ScopeWorkspace, ids.Nil, nil
}
