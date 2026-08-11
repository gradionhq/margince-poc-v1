// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package channels

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/shared/ports/jurisdiction"
)

// The statutory retention floor, armed for THIS binary. The jurisdiction registry
// is process-global and one package is one binary, so the parent package's own
// arming does not reach here: this file is the registration travelling with the
// erasure suite that moved out of internal/compose/integration.
//
// Do not rename this file to anything ending in a GOOS or GOARCH word. Go reads
// the segment before _test.go as an implicit build constraint, so a plausible
// name like jurisdiction_arm_test.go is silently dropped on every machine whose
// GOARCH is not literally arm — the registration then never happens, and the
// suite below reports the missing floor as a missing shield.
func init() {
	integration.RegisterGoBDFloorPack()
}

// TestTheStatutoryFloorIsArmedForThisBinary is the guard the erasure suites
// cannot be: an absent floor makes the destructive path succeed, so a suite that
// asserts the shield would go green for the opposite of the intended reason.
// This asserts the registration itself, where a failure names its own cause.
//
// It asserts THIS pack and the span the suites were written against, not merely
// that some pack declares a correspondence class. A different pack satisfying the
// weaker check would let this pass while the floor the erasure assertions depend
// on is absent or shortened — the same false green by another route.
func TestTheStatutoryFloorIsArmedForThisBinary(t *testing.T) {
	want := integration.GoBDFloorPack{}
	for _, pack := range jurisdiction.Applicable() {
		if pack.Code() != want.Code() {
			continue
		}
		retention := pack.Retention()
		if retention == nil {
			t.Fatalf("pack %q is registered but declares no retention", pack.Code())
		}
		for _, class := range retention.Classes() {
			if class.Name != jurisdiction.CommercialCorrespondence {
				continue
			}
			if class.Keep.Years != 6 || class.Anchor != jurisdiction.AnchorCalendarYearEnd {
				t.Fatalf("the correspondence floor is %+v, want six years anchored to calendar-year end — the erasure suites here are written against that span",
					class)
			}
			return
		}
		t.Fatalf("pack %q declares no commercial-correspondence class", pack.Code())
	}
	t.Fatalf("pack %q is not registered in this binary — the erasure suites here would pass by proving nothing", want.Code())
}
