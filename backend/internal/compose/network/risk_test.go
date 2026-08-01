// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package network

// The risk rules against hand-built facts and a fixed clock. The fold is pure,
// so these need no database — and that is the point: a threshold is a claim
// about a number, and a test that has to seed a deal to check it is testing
// the seeding.

import (
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// testNow is the fixed instant every fold in this file is judged at. A real
// clock would make the going-cold assertions pass or fail by the day.
var testNow = time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)

func seat(engaged bool, role string) deals.DealStakeholder {
	return deals.DealStakeholder{PersonID: ids.NewV7(), Role: role, Engaged: engaged}
}

// daysAgo is the clock arithmetic the going-cold tests read in.
func daysAgo(n int) time.Time { return testNow.AddDate(0, 0, -n) }

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
	if !kinds(foldRisks(one, testNow))[RiskSingleThreadedTheirs] {
		t.Error("a deal with one engaged contact and two idle seats is not flagged single-threaded")
	}

	// Two engaged clears it, exactly at the floor.
	two := DealCoverage{DealID: ids.NewV7(), Stakeholders: []deals.DealStakeholder{
		seat(true, roleChampion), seat(true, "user"),
	}}
	if kinds(foldRisks(two, testNow))[RiskSingleThreadedTheirs] {
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
	if kinds(foldRisks(young, testNow))[RiskSingleThreadedOurs] {
		t.Errorf("a deal with %d total interactions flagged as concentrated; the minimum is %d",
			2, ourSideMinInteractions)
	}

	// A real one: plenty of contact, almost all of it one person's.
	concentrated := DealCoverage{DealID: ids.NewV7(), Stakeholders: base, OurSide: []ColleagueEdge{
		{UserID: rep, PersonID: champion.PersonID, Count90d: 18},
		{UserID: ids.NewV7(), PersonID: other.PersonID, Count90d: 1},
	}}
	risks := foldRisks(concentrated, testNow)
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
	if kinds(foldRisks(shared, testNow))[RiskSingleThreadedOurs] {
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
	got := kinds(foldRisks(noChampion, testNow))
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
	if !kinds(foldRisks(quietChampion, testNow))[RiskCoverageGap] {
		t.Error("an unengaged champion counted as an engaged one — a name on a seat is not advocacy")
	}
}

func TestADealWithNoSeatsAtAllRaisesNoCoverageGap(t *testing.T) {
	// An empty deal is early, not uncovered. Flagging it would put a risk chip
	// on every deal the moment it is created, and a warning that is always on
	// is a warning nobody reads.
	empty := DealCoverage{DealID: ids.NewV7()}
	if kinds(foldRisks(empty, testNow))[RiskCoverageGap] {
		t.Error("a deal with no stakeholders yet is flagged for having no champion")
	}
}

func TestGoingColdFiresOnTheReportingWindowAndOnlyWhileTheDealIsOpen(t *testing.T) {
	base := []deals.DealStakeholder{seat(true, roleChampion), seat(true, "user")}
	cover := func(status string, touched int) DealCoverage {
		return DealCoverage{
			DealID: ids.NewV7(), Status: status,
			LastTouchAt: daysAgo(touched), Stakeholders: base,
		}
	}

	// One day inside the window is not cold. A flag that fires at 29 days
	// contradicts every other surface that reads REPORT-PARAM-2.
	if kinds(foldRisks(cover(dealStatusOpen, goingColdDays-1), testNow))[RiskGoingCold] {
		t.Errorf("a deal touched %d days ago flagged going cold; the window is %d",
			goingColdDays-1, goingColdDays)
	}

	// Exactly at the window is.
	risks := foldRisks(cover(dealStatusOpen, goingColdDays), testNow)
	if !kinds(risks)[RiskGoingCold] {
		t.Fatalf("a deal untouched for %d days is not flagged going cold", goingColdDays)
	}
	// The day count is the finding's evidence, and it is what the 60-day view
	// filters on — a risk that only said "cold" could not drive that segment.
	for _, r := range risks {
		if r.Kind == RiskGoingCold && r.DaysSinceTouch != goingColdDays {
			t.Errorf("going-cold reports %d days since touch, want %d", r.DaysSinceTouch, goingColdDays)
		}
	}

	// A closed deal is silent because it is finished.
	for _, status := range []string{"won", "lost"} {
		if kinds(foldRisks(cover(status, 400), testNow))[RiskGoingCold] {
			t.Errorf("a %s deal untouched for 400 days flagged going cold — it was delivered, not lost", status)
		}
	}
}

func TestGoingColdSaysNothingAboutADealWhoseTouchWasNeverGathered(t *testing.T) {
	// A zero LastTouchAt means the gather did not run, not that nobody has
	// spoken since the epoch. Reading it as the second would flag every deal in
	// any fixture that never described one.
	ungathered := DealCoverage{
		DealID: ids.NewV7(), Status: dealStatusOpen,
		Stakeholders: []deals.DealStakeholder{seat(true, roleChampion), seat(true, "user")},
	}
	if kinds(foldRisks(ungathered, testNow))[RiskGoingCold] {
		t.Error("a coverage view with no gathered last touch was flagged going cold")
	}
}

func TestAChampionLeavingIsNotTheSameFindingAsAnyoneElseLeaving(t *testing.T) {
	champion := seat(true, roleChampion)
	legal := seat(true, "legal")
	user := seat(true, "user")

	// Only the champion has left.
	championGone := DealCoverage{
		DealID: ids.NewV7(), Stakeholders: []deals.DealStakeholder{champion, legal, user},
		DepartedPersonIDs: []ids.UUID{champion.PersonID},
	}
	risks := foldRisks(championGone, testNow)
	got := kinds(risks)
	if !got[RiskChampionLeft] {
		t.Error("the champion leaving the account is not flagged as champion_left")
	}
	if got[RiskStakeholderLeft] {
		t.Error("the champion's departure ALSO raised the milder stakeholder_left — one departure, one finding")
	}
	for _, r := range risks {
		if r.Kind != RiskChampionLeft {
			continue
		}
		// The finding must name WHO left, or a rep cannot go and replace them.
		if len(r.PersonIDs) != 1 || r.PersonIDs[0] != champion.PersonID {
			t.Errorf("champion_left names %v, want the departed champion %s", r.PersonIDs, champion.PersonID)
		}
	}

	// Two other seats have left; the champion is still there.
	othersGone := DealCoverage{
		DealID: ids.NewV7(), Stakeholders: []deals.DealStakeholder{champion, legal, user},
		DepartedPersonIDs: []ids.UUID{user.PersonID, legal.PersonID},
	}
	risks = foldRisks(othersGone, testNow)
	got = kinds(risks)
	if !got[RiskStakeholderLeft] {
		t.Error("two stakeholders leaving the account is not flagged as stakeholder_left")
	}
	if got[RiskChampionLeft] {
		t.Error("a non-champion departure was reported as the champion leaving")
	}
	// Ordering follows the deal's own seat order, so two reads of an unchanged
	// deal render the same list.
	for _, r := range risks {
		if r.Kind != RiskStakeholderLeft {
			continue
		}
		want := []ids.UUID{legal.PersonID, user.PersonID}
		if len(r.PersonIDs) != len(want) {
			t.Fatalf("stakeholder_left names %d people, want %d", len(r.PersonIDs), len(want))
		}
		for i, id := range want {
			if r.PersonIDs[i] != id {
				t.Errorf("stakeholder_left[%d] = %s, want %s (seat order, not departure order)", i, r.PersonIDs[i], id)
			}
		}
	}
}

func TestADepartureIsOnlyReportedForASeatTheDealActuallyHas(t *testing.T) {
	// The departed set is gathered per account, so it can name somebody who
	// left the company and was never on this deal. Reporting them would put a
	// stranger's name on a rep's risk chip.
	stranger := ids.NewV7()
	c := DealCoverage{
		DealID:            ids.NewV7(),
		Stakeholders:      []deals.DealStakeholder{seat(true, roleChampion), seat(true, "user")},
		DepartedPersonIDs: []ids.UUID{stranger},
	}
	got := kinds(foldRisks(c, testNow))
	if got[RiskChampionLeft] || got[RiskStakeholderLeft] {
		t.Error("a departure was reported for somebody who holds no seat on this deal")
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
	risks := foldRisks(c, testNow)
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
