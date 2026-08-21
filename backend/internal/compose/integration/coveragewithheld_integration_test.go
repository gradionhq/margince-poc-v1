// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// A coverage view assembled for a caller without the edge grant, against a deal
// that really does have a seat and really does carry a finding.
//
// The unit tests prove the refusal resolves before any statement. What only a
// database can prove is the half that matters to a person: the SAME deal, read
// by two callers who differ in one grant, comes back as a finding for one and as
// a NAMED omission for the other — never as the clean verdict an empty risk list
// renders as.
//
// Seeded through the module stores that serve the HTTP writers, so the seat and
// the employment are the rows production writes. A hand-inserted edge would
// prove something about this test's SQL rather than about the product.

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/network"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// coverageReaderPerms is everything the coverage read asks for except the edge,
// which withEdge adds. The two callers below differ in exactly one grant, so any
// difference in what they are served is that grant's doing and nothing else's.
func coverageReaderPerms(withEdge bool) principal.Permissions {
	objects := map[string]principal.ObjectGrant{
		"deal":         {Read: true},
		"person":       {Read: true},
		"organization": {Read: true},
		"activity":     {Read: true},
	}
	if withEdge {
		objects["relationship"] = principal.ObjectGrant{Read: true}
	}
	return principal.Permissions{
		RoleKeys: []string{"rep"},
		Objects:  objects,
		RowScope: principal.RowScopeAll,
	}
}

func TestCoverageIsWithheldRatherThanReportedCleanWithoutTheEdgeGrant(t *testing.T) {
	e := Setup(t)
	org := e.SeedOrg(t, "Kessler Systems", nil)
	orgID := ids.From[ids.OrganizationKind](org)

	// A champion whose employment has ENDED: a real finding on a real seat, so
	// the withheld read is compared against something rather than against
	// silence. The end date is in the past relative to the clock below.
	gone := e.SeedPerson(t, "Departed Champion", nil)
	personID := ids.From[ids.PersonKind](gone)
	started := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	ended := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
	if _, err := e.People.CreateRelationship(e.Admin(), people.CreateRelationshipInput{
		Kind: "employment", PersonID: &personID, OrganizationID: &orgID,
		StartedAt: &started, EndedAt: &ended, Source: "manual",
	}); err != nil {
		t.Fatalf("recording the ended employment: %v", err)
	}

	pipeline, open := pipelineFixtureFor(e.Admin(), t, e.Deals)
	deal, err := e.Deals.CreateDeal(e.Admin(), deals.CreateDealInput{
		Name: "Renewal", PipelineID: pipeline, StageID: open,
		OrganizationID: &orgID, Source: "manual",
	})
	if err != nil {
		t.Fatalf("creating the deal: %v", err)
	}
	dealID := ids.From[ids.DealKind](ids.UUID(deal.Id))
	champion := "champion"
	if _, err := e.People.CreateRelationship(e.Admin(), people.CreateRelationshipInput{
		Kind: "deal_stakeholder", PersonID: &personID, DealID: &dealID,
		Role: &champion, Source: "manual",
	}); err != nil {
		t.Fatalf("seating the champion: %v", err)
	}

	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	granted := coverageFor(t, e, coverageReaderPerms(true), dealID, now)
	if len(granted.SectionsOmitted) != 0 {
		t.Fatalf("the granted caller was told %v was withheld — the fixture then proves nothing "+
			"about the withheld caller", granted.SectionsOmitted)
	}
	if len(granted.Stakeholders) != 1 || len(granted.Risks) == 0 {
		t.Fatalf("the granted caller sees %d seats and %d risks, want the seat and its finding",
			len(granted.Stakeholders), len(granted.Risks))
	}

	withheld := coverageFor(t, e, coverageReaderPerms(false), dealID, now)
	want := []string{network.SectionStakeholders, network.SectionOurSide, network.SectionRisks}
	if len(withheld.SectionsOmitted) != len(want) {
		t.Fatalf("the withheld caller is told %v was omitted, want %v — an empty risk list with "+
			"nothing naming it renders as \"this deal passes every coverage check\"",
			withheld.SectionsOmitted, want)
	}
	for i, section := range want {
		if withheld.SectionsOmitted[i] != section {
			t.Errorf("omitted[%d] = %q, want %q", i, withheld.SectionsOmitted[i], section)
		}
	}
	// Named AND empty. A withheld section carrying a partial answer would be
	// worse than either honest state, because nothing could say which it was.
	if len(withheld.Stakeholders) != 0 || len(withheld.OurSide) != 0 || len(withheld.Risks) != 0 {
		t.Errorf("the withheld view still carries %d seats, %d colleagues and %d risks",
			len(withheld.Stakeholders), len(withheld.OurSide), len(withheld.Risks))
	}
}

func coverageFor(
	t *testing.T, e *Env, perms principal.Permissions, dealID ids.DealID, now time.Time,
) network.DealCoverage {
	t.Helper()
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, perms)
	var out network.DealCoverage
	err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		coverage, err := network.CoverageFor(ctx, tx, dealID, now)
		out = coverage
		return err
	})
	if err != nil {
		t.Fatalf("assembling the coverage view: %v", err)
	}
	return out
}
