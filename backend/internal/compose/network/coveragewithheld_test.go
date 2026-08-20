// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package network

// A coverage view without the edge grant answers "withheld", never "clean".
//
// The defect these tests hold shut is not a disclosure — it is the fix for one.
// Gating the coverage reads without a channel to report the gating would have
// left every restricted caller looking at an empty risk list, which this
// product renders as "Nothing flagged — this deal passes every coverage check".
// A wrong verdict on deal risk is worse than the pair it stopped disclosing.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// coverageReaderWithoutTheEdgeGrant holds every grant the coverage endpoint
// asks for except the edge: the shape an operator produces by restricting
// relationship access on a role that still works deals.
func coverageReaderWithoutTheEdgeGrant() context.Context {
	return principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:test", UserID: ids.NewV7(),
		Permissions: principal.Permissions{
			RoleKeys: []string{"rep"},
			Objects: map[string]principal.ObjectGrant{
				"deal":         {Read: true},
				"person":       {Read: true},
				"organization": {Read: true},
			},
			RowScope: principal.RowScopeAll,
		},
	})
}

// The nil transaction is the assertion. CoverageFor's first act must be the
// admission, before the deal row, the seats or the departures — a version that
// assembled the payload and filtered afterwards would have read every row it
// then withheld, and no assertion about the returned payload could tell the
// difference.
func TestCoverageNamesTheWithheldSectionsWithoutReachingAStatement(t *testing.T) {
	coverage, err := CoverageFor(coverageReaderWithoutTheEdgeGrant(), nil,
		ids.From[ids.DealKind](ids.NewV7()), time.Now().UTC())
	if err != nil {
		t.Fatalf("CoverageFor(no edge grant) = %v, want a withheld payload and no error", err)
	}
	want := []string{SectionStakeholders, SectionOurSide, SectionRisks}
	if len(coverage.SectionsOmitted) != len(want) {
		t.Fatalf("sections omitted = %v, want %v", coverage.SectionsOmitted, want)
	}
	for i, section := range want {
		if coverage.SectionsOmitted[i] != section {
			t.Errorf("sections omitted[%d] = %q, want %q", i, coverage.SectionsOmitted[i], section)
		}
	}
	// The three sections are EMPTY as well as named. A withheld section that
	// carried a partial answer would leave a client unable to say whether what
	// it holds is all there is.
	if len(coverage.Stakeholders) != 0 || len(coverage.OurSide) != 0 || len(coverage.Risks) != 0 {
		t.Errorf("a withheld coverage view still carries content: %d seats, %d colleagues, %d risks",
			len(coverage.Stakeholders), len(coverage.OurSide), len(coverage.Risks))
	}
}

// The departure read is reached only after the seat read passed the same gate,
// so this refusal never fires in production today. It is asserted because the
// safety of the read would otherwise rest on the order its package happens to
// call things in, and that is one refactor from a disclosure.
func TestTheDepartureReadRefusesBeforeItReachesAStatement(t *testing.T) {
	_, err := readDeparted(coverageReaderWithoutTheEdgeGrant(), nil,
		ids.NewV7(), []ids.UUID{ids.NewV7()}, time.Now().UTC())
	if !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("readDeparted(no edge grant) = %v, want ErrPermissionDenied", err)
	}
}

// The wire payload carries the omission as an array that is present and empty
// on the ordinary read. Absent would make a client guess at the difference
// between "nothing was withheld" and "the server did not say".
func TestTheWirePayloadCarriesTheOmissionAndIsNeverNull(t *testing.T) {
	withheld := wireCoverage(DealCoverage{
		DealID:          ids.NewV7(),
		SectionsOmitted: []string{SectionStakeholders, SectionOurSide, SectionRisks},
	}, nil)
	if len(withheld.SectionsOmitted) != 3 {
		t.Errorf("the withheld payload names %v, want three sections", withheld.SectionsOmitted)
	}
	ordinary := wireCoverage(DealCoverage{DealID: ids.NewV7()}, nil)
	if ordinary.SectionsOmitted == nil {
		t.Error("the ordinary payload's sections_omitted is null, want an empty array")
	}
	if len(ordinary.SectionsOmitted) != 0 {
		t.Errorf("the ordinary payload names %v withheld, want none", ordinary.SectionsOmitted)
	}
}
