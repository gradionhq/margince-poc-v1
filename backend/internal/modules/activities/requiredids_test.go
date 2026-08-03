// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// A required body id the caller omitted must be named as a missing argument, not
// discovered as a missing row: an absent key decodes to the zero UUID with no
// error, so unguarded it reaches a lookup, matches nothing, and answers a bare
// not-found for a record the caller never mentioned.
//
// The guard is at the store entry point — the door every transport comes through —
// and it runs BEFORE any authority check or query, which is why these probes need
// no database and no actor: a store over a nil pool never reaches one.
//
// The refusal's SHAPE is proven once in platform/httperr/requirebodyid_test.go and
// asserted here through faulttest. What is left is the only question this package
// can answer: is the guard actually called for my body.

import (
	"context"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/httperr/faulttest"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestAnOmittedRelinkTargetIsNamed(t *testing.T) {
	// RelinkActivityJSONBody.entity_id. Relinking moves an activity ONTO a
	// record; with no entity_id there is nowhere to move it, and the zero UUID
	// reached the link-target gate instead.
	_, err := NewStore(nil).RelinkActivity(context.Background(), ids.New[ids.ActivityKind](), RelinkActivityInput{
		EntityType: "person",
	})
	faulttest.AssertNamesOmittedID(t, err, "entity_id")
}
