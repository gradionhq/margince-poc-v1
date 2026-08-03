// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

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

func TestAnOmittedMergeTargetOrStakeholderIsNamed(t *testing.T) {
	// MergePersonJSONBody.target_id, MergeOrganizationJSONBody.target_id and
	// SetProjectStakeholderRequest.person_id.
	//
	// The merge pair is the sharp case: the self-merge guard next to it does NOT
	// catch an omitted target, because a real source id never equals the zero
	// one — so the zero id reached the pair lock and answered not-found for a
	// survivor nobody named.
	store := NewStore(nil)
	ctx := context.Background()

	_, err := store.MergePerson(ctx, ids.New[ids.PersonKind](), ids.PersonID{})
	faulttest.AssertNamesOmittedID(t, err, "target_id")

	_, err = store.MergeOrganization(ctx, ids.New[ids.OrganizationKind](), ids.OrganizationID{})
	faulttest.AssertNamesOmittedID(t, err, "target_id")

	_, err = store.SetProjectStakeholder(ctx, SetProjectStakeholderInput{
		ProjectID: ids.New[ids.ProjectKind](), Role: "sponsor",
	})
	faulttest.AssertNamesOmittedID(t, err, "person_id")
}
