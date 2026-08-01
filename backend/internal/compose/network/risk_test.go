// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package network

// The risk rules against hand-built facts and a fixed clock. The fold is pure,
// so these need no database — and that is the point: a threshold is a claim
// about a number, and a test that has to seed a deal to check it is testing
// the seeding.

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func seat(engaged bool, role string) deals.DealStakeholder {
	return deals.DealStakeholder{PersonID: ids.NewV7(), Role: role, Engaged: engaged}
}

func kinds(risks []Risk) map[string]bool {
	out := map[string]bool{}
	for _, r := range risks {
		out[r.Kind] = true
	}
	return out
}

func TestSingleThreadedIsTheReportingRuleVerbatim(t *testing.T) {
	// REPORT-PARAM-1: distinct_engaged_contacts < 2. One engaged contact is
	// single-threaded however many seats the deal has — an unengaged seat is
	// a name on a list, not a relationship.
	one := DealCoverage{DealID: ids.NewV7(), Stakeholders: []deals.DealStakeholder{
		seat(true, roleChampion), seat(false, "user"), seat(false, "legal"),
	}}
	if !kinds(foldRisks(one))[RiskSingleThreadedTheirs] {
		t.Error("a deal with one engaged contact and two idle seats is not flagged single-threaded")
	}

	// Two engaged clears it, exactly at the floor.
	two := DealCoverage{DealID: ids.NewV7(), Stakeholders: []deals.DealStakeholder{
		seat(true, roleChampion), seat(true, "user"),
	}}
	if kinds(foldRisks(two))[RiskSingleThreadedTheirs] {
		t.Errorf("two engaged contacts flagged single-threaded — the floor is %d, and a flag that fires at the boundary contradicts every other surface", reportThreadingFloor)
	}
}

func TestOurSideConcentrationNeedsBothVolumeAndDominance(t *testing.T) {
	champion := seat(true, roleChampion)
	other := seat(true, "user")
	base := []deals.DealStakeholder{champion, other}
	rep := ids.NewV7()

	// A young deal: one colleague, but only two interactions ever. Flagging
	// this would tell a rep their brand-new deal is dangerously concentrated.
	young := DealCoverage{DealID: ids.NewV7(), Stakeholders: base, OurSide: []ColleagueEdge{
		{UserID: rep, PersonID: champion.PersonID, Count90d: 2},
	}}
	if kinds(foldRisks(young))[RiskSingleThreadedOurs] {
		t.Errorf("a deal with %d total interactions flagged as concentrated; the minimum is %d",
			2, ourSideMinInteractions)
	}

	// A real one: plenty of contact, almost all of it one person's.
	concentrated := DealCoverage{DealID: ids.NewV7(), Stakeholders: base, OurSide: []ColleagueEdge{
		{UserID: rep, PersonID: champion.PersonID, Count90d: 18},
		{UserID: ids.NewV7(), PersonID: other.PersonID, Count90d: 1},
	}}
	risks := foldRisks(concentrated)
	if !kinds(risks)[RiskSingleThreadedOurs] {
		t.Fatal("18 of 19 interactions by one colleague is not flagged as our-side concentration")
	}
	// The finding must name WHO, or a rep cannot act on it.
	for _, r := range risks {
		if r.Kind == RiskSingleThreadedOurs {
			if len(r.UserIDs) != 1 || r.UserIDs[0] != rep {
				t.Errorf("the concentration risk names %v, want the carrying colleague %s", r.UserIDs, rep)
			}
		}
	}

	// Shared evenly across two colleagues is not a risk.
	shared := DealCoverage{DealID: ids.NewV7(), Stakeholders: base, OurSide: []ColleagueEdge{
		{UserID: rep, PersonID: champion.PersonID, Count90d: 10},
		{UserID: ids.NewV7(), PersonID: other.PersonID, Count90d: 10},
	}}
	if kinds(foldRisks(shared))[RiskSingleThreadedOurs] {
		t.Error("evenly shared contact flagged as carried by one colleague")
	}
}

func TestACoverageGapIsAboutTheChampionNotTheCount(t *testing.T) {
	// Three engaged contacts and no champion among them: well covered by the
	// threading rule, and still nobody inside is arguing for the deal. The two
	// findings are different questions and must not collapse into one.
	noChampion := DealCoverage{DealID: ids.NewV7(), Stakeholders: []deals.DealStakeholder{
		seat(true, "user"), seat(true, "legal"), seat(true, "finance"),
	}}
	got := kinds(foldRisks(noChampion))
	if !got[RiskCoverageGap] {
		t.Error("three engaged contacts with no champion is not flagged as a coverage gap")
	}
	if got[RiskSingleThreadedTheirs] {
		t.Error("a well-threaded deal was also flagged single-threaded")
	}

	// A champion who exists but has gone quiet does not count: the seat is not
	// the relationship.
	quietChampion := DealCoverage{DealID: ids.NewV7(), Stakeholders: []deals.DealStakeholder{
		seat(false, roleChampion), seat(true, "user"), seat(true, "legal"),
	}}
	if !kinds(foldRisks(quietChampion))[RiskCoverageGap] {
		t.Error("an unengaged champion counted as an engaged one — a name on a seat is not advocacy")
	}
}

func TestADealWithNoSeatsAtAllRaisesNoCoverageGap(t *testing.T) {
	// An empty deal is early, not uncovered. Flagging it would put a risk chip
	// on every deal the moment it is created, and a warning that is always on
	// is a warning nobody reads.
	empty := DealCoverage{DealID: ids.NewV7()}
	if kinds(foldRisks(empty))[RiskCoverageGap] {
		t.Error("a deal with no stakeholders yet is flagged for having no champion")
	}
}

func TestEveryRiskCarriesADealAndAReason(t *testing.T) {
	// A risk without a deal cannot be rendered, and one without a summary is
	// a red dot nobody can act on.
	c := DealCoverage{
		DealID:       ids.NewV7(),
		Stakeholders: []deals.DealStakeholder{seat(true, "user")},
		OurSide:      []ColleagueEdge{{UserID: ids.NewV7(), Count90d: 20}},
	}
	risks := foldRisks(c)
	if len(risks) == 0 {
		t.Fatal("the fixture produced no risks; the assertions below would pass vacuously")
	}
	for _, r := range risks {
		if r.DealID == ids.Nil {
			t.Errorf("risk %q carries no deal", r.Kind)
		}
		if r.Summary == "" {
			t.Errorf("risk %q carries no reason a human could act on", r.Kind)
		}
	}
}
