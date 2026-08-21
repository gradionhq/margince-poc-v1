// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// What the refusing incumbent's error MEANS to the code that reads it, which is
// a separate question from whether it refuses.
//
// jobs_overlay.go's isConnectionLevelIncumbentError decides whether a reconcile
// sweep aborts and backs the connection off, or logs one object class and
// carries on. An error it does not recognise takes the second branch — so a
// refusal it cannot classify would let the fleet worker sweep a connection where
// EVERY call refused, treat each refusal as that class's own data defect, and
// finish by recording the sweep a success. A green sweep that did nothing is the
// precise false green this build exists to remove, so the classification is
// pinned here rather than left to the wrapping to imply.
//
// Needs no database despite the tag: the behaviour under test exists only in
// this build.

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/overlay"
)

// refusals is every way the connect and reconcile paths can reach this
// incumbent. Enumerated rather than asserted through errNoLiveIncumbent
// directly, because the claim is about the METHODS: a later one that forgets to
// refuse, or refuses with a bare error of its own, is the regression, and
// comparing the sentinel to itself could never see it.
//
// A hand-written list is a census, and a census goes stale in silence — adding a
// method to overlay.Incumbent forces refusingIncumbent to implement it, but
// nothing would force it in here, so the new method would ship with this test
// green. TestTheRefusalCensusCoversEveryMethod holds the list to the interface.
func refusals(inc refusingIncumbent) map[string]error {
	ctx := context.Background()
	epoch := time.Time{}
	_, backfillErr := inc.Backfill(ctx, "person", "")
	_, modifiedErr := inc.Modified(ctx, "person", epoch, "")
	_, deletionsErr := inc.Deletions(ctx, "person", epoch, "")
	_, getErr := inc.Get(ctx, "person", "1")
	_, assocErr := inc.Associations(ctx, "person", "1", "organization")
	_, ownerEmailErr := inc.OwnerEmail(ctx, "owner-1")
	_, ownersErr := inc.Owners(ctx)
	_, accountErr := inc.AccountID(ctx)
	_, createErr := inc.Create(ctx, "person", nil)
	_, updateErr := inc.Update(ctx, "person", "1", nil, epoch)
	return map[string]error{
		"Backfill":     backfillErr,
		"Modified":     modifiedErr,
		"Deletions":    deletionsErr,
		"Get":          getErr,
		"Associations": assocErr,
		"OwnerEmail":   ownerEmailErr,
		"Owners":       ownersErr,
		"AccountID":    accountErr,
		"Create":       createErr,
		"Update":       updateErr,
		"Archive":      inc.Archive(ctx, "person", "1", epoch),
	}
}

func TestEveryRefusalReadsAsAWholeConnectionFailure(t *testing.T) {
	for method, err := range refusals(refusingIncumbent{}) {
		if err == nil {
			t.Errorf("%s answered instead of refusing — the integration build binds no live incumbent, so nothing here can have an answer to give", method)
			continue
		}
		if !isConnectionLevelIncumbentError(err) {
			t.Errorf("%s refused with an error the reconcile sweep reads as one object class's data defect: %v.\n"+
				"A sweep where every call refuses would then record itself a success.", method, err)
		}
	}
}

// The census's own gate. Without it, the test above asserts a property of
// whichever methods somebody remembered — which is the shape it exists to catch,
// one level up.
func TestTheRefusalCensusCoversEveryMethod(t *testing.T) {
	seam := reflect.TypeOf((*overlay.Incumbent)(nil)).Elem()
	covered := refusals(refusingIncumbent{})
	for i := range seam.NumMethod() {
		name := seam.Method(i).Name
		if name == "Name" {
			// Reports the incumbent's identity; it has nothing to refuse.
			continue
		}
		if _, ok := covered[name]; !ok {
			t.Errorf("overlay.Incumbent.%s is not in the refusal census — it can refuse with an error "+
				"the reconcile sweep reads as one object class's data defect and nothing here would say so", name)
		}
	}
}
