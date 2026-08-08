// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package orgdossier

// DOSS-FORM-2 as a suite: what counts as a populated input, when the assembly
// must abstain, and what caps a band it was otherwise willing to give.
//
// The abstention cases matter more than the confident ones. A band that comes
// out too high on thin evidence is the failure this formula exists to prevent,
// and it is the one that looks correct in a screenshot.

import (
	"testing"
	"time"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// A fixed clock: the freshness window is a real boundary in this formula, so
// the tests name both sides of it rather than leaning on the wall clock.
var assessedAt = time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC)

func machineField(name, value string, retrieved time.Time) crmcontracts.CompanyProfileField {
	return crmcontracts.CompanyProfileField{
		Id:          rowID(),
		Field:       crmcontracts.CompanyProfileFieldField(name),
		Value:       value,
		Source:      crmcontracts.CompanyProfileFieldSource(crmcontracts.CompanyProfileFieldSourceSiteRead),
		RetrievedAt: &retrieved,
		UpdatedAt:   retrieved,
	}
}

func machineFact(name, value string, retrieved time.Time) crmcontracts.OrganizationFact {
	id := rowID()
	return crmcontracts.OrganizationFact{
		Id:          id,
		Field:       crmcontracts.OrganizationFactField(name),
		Value:       value,
		Source:      crmcontracts.OrganizationFactSource(crmcontracts.OrganizationFactSourceSiteRead),
		RetrievedAt: &retrieved,
		UpdatedAt:   retrieved,
	}
}

// fourOfSeven is the spec's own worked example: four required inputs populated,
// three missing.
func fourOfSeven() Input {
	fresh := assessedAt.Add(-24 * time.Hour)
	return Input{
		OrganizationID: ids.NewV7().String(),
		ProfileFields: []crmcontracts.CompanyProfileField{
			machineField("offer_summary", "Load-shifting software for industrial sites", fresh),
			machineField("icp", "Energy-intensive manufacturers", fresh),
			machineField("industry", "Industrial software", fresh),
		},
		Facts: []crmcontracts.OrganizationFact{
			machineFact("employee_range", "51-200", fresh),
		},
	}
}

// The denominator is a claim in its own right. "4 of 7" and "4 of 40" describe
// completely different levels of knowledge, and a surface that dropped the
// denominator would render them identically (DOSS-AC-12).
func TestCompletenessReportsBothCountsAndNamesWhatIsMissing(t *testing.T) {
	got := Completeness(fourOfSeven(), assessedAt)

	if got.Present != 4 {
		t.Errorf("present = %d, want 4", got.Present)
	}
	if got.Expected != 7 {
		t.Errorf("expected = %d, want 7", got.Expected)
	}
	if got.Missing == nil {
		t.Fatal("missing is nil — the named gaps ARE the next-step recommendation")
	}
	if len(*got.Missing) != 3 {
		t.Errorf("missing = %v, want the 3 unpopulated inputs named", *got.Missing)
	}
	for _, named := range *got.Missing {
		if named == "" {
			t.Error("an unnamed missing input tells the reader nothing to go and find")
		}
	}
}

// The abstention is the whole point of the floor: below it the band is
// `unknown` whatever the few available facts suggest, and the reader is handed
// a next step instead of a score (DOSS-AC-12, DOSS-SEED-2).
func TestBelowTheFloorTheBandIsUnknownEvenWhenTheFactsLookStrong(t *testing.T) {
	fresh := assessedAt.Add(-24 * time.Hour)
	twoOfSeven := Input{
		OrganizationID: ids.NewV7().String(),
		ProfileFields: []crmcontracts.CompanyProfileField{
			machineField("offer_summary", "Load-shifting software", fresh),
			machineField("icp", "Energy-intensive manufacturers", fresh),
		},
	}

	got := Assess(twoOfSeven, crmcontracts.GrowthFitBandStrong, true, AbstainedNoWriter, assessedAt)

	if got.Band != crmcontracts.GrowthFitBandUnknown {
		t.Errorf("band = %q, want unknown — two of seven inputs cannot support a judgment", got.Band)
	}
	if got.NextStep == "" {
		t.Error("an abstention with no next step tells the reader nothing they can act on")
	}
	if got.CappedReason != "" {
		t.Errorf("capped reason = %q, want empty — the floor declined to judge, it did not cap a judgment", got.CappedReason)
	}
}

// A fit computed against a guess about ourselves is a guess about them, so an
// unconfirmed workspace offering caps the band and says so (DOSS-AC-13).
func TestAnUnconfirmedWorkspaceOfferingCapsTheBandAtModerate(t *testing.T) {
	got := Assess(fourOfSeven(), crmcontracts.GrowthFitBandStrong, false, AbstainedNoWriter, assessedAt)

	if got.Band != crmcontracts.GrowthFitBandModerate {
		t.Errorf("band = %q, want moderate — a strong fit cannot be justified against an unconfirmed offering", got.Band)
	}
	if got.CappedReason == "" {
		t.Error("the band was lowered and the surface does not say why")
	}
	if got.NextStep == "" {
		t.Error("the reader is not told that confirming their own profile is what lifts the cap")
	}
}

// The same company, with our own offering confirmed, keeps the band it earned.
// This is the other half of the worked example and the mutation check on the
// test above: without it, a cap that fired unconditionally would still pass.
func TestAConfirmedWorkspaceOfferingLeavesTheBandAlone(t *testing.T) {
	got := Assess(fourOfSeven(), crmcontracts.GrowthFitBandStrong, true, AbstainedNoWriter, assessedAt)

	if got.Band != crmcontracts.GrowthFitBandStrong {
		t.Errorf("band = %q, want strong — four of seven inputs clears the floor", got.Band)
	}
	if got.CappedReason != "" {
		t.Errorf("capped reason = %q, want empty — nothing capped this band", got.CappedReason)
	}
	if got.NextStep != "" {
		t.Errorf("next step = %q, want empty — nothing is holding this answer back", got.NextStep)
	}
}

// The cap lowers a band; it does not annotate one that was already lower.
// `band_capped_reason` is documented as null when nothing capped it, and a
// reason attached to an unchanged `weak` would claim a ceiling that never bit.
func TestABandAlreadyBelowTheCapCarriesNoCappedReason(t *testing.T) {
	got := Assess(fourOfSeven(), crmcontracts.GrowthFitBandWeak, false, AbstainedNoWriter, assessedAt)

	if got.Band != crmcontracts.GrowthFitBandWeak {
		t.Errorf("band = %q, want weak — the cap must not raise a band", got.Band)
	}
	if got.CappedReason != "" {
		t.Errorf("capped reason = %q, want empty — weak is already below the moderate cap", got.CappedReason)
	}
}

// Staleness is not cosmetic here: a value read from a website two years ago may
// describe a company that no longer exists in that shape, and letting it hold
// up a band is exactly how a confident answer gets built on nothing.
func TestAStaleMachineReadValueDoesNotCountTowardCompleteness(t *testing.T) {
	stale := assessedAt.Add(-40 * 24 * time.Hour)
	in := Input{
		OrganizationID: ids.NewV7().String(),
		ProfileFields: []crmcontracts.CompanyProfileField{
			machineField("offer_summary", "Load-shifting software", stale),
		},
	}

	if got := Completeness(in, assessedAt); got.Present != 0 {
		t.Errorf("present = %d, want 0 — the one value was read %v ago", got.Present, assessedAt.Sub(stale))
	}
}

// A person who typed the answer did not read a source, so there is nothing to
// re-read and nothing to go stale. Expiring their entry would ask them to
// retype the same fact on a schedule.
func TestAHumanEnteredValueNeverGoesStale(t *testing.T) {
	longAgo := assessedAt.Add(-5 * 365 * 24 * time.Hour)
	in := Input{
		OrganizationID: ids.NewV7().String(),
		ProfileFields: []crmcontracts.CompanyProfileField{{
			Id:        rowID(),
			Field:     crmcontracts.CompanyProfileFieldField("offer_summary"),
			Value:     "Load-shifting software",
			Source:    crmcontracts.CompanyProfileFieldSource(crmcontracts.CompanyProfileFieldSourceHuman),
			UpdatedAt: longAgo,
		}},
	}

	if got := Completeness(in, assessedAt); got.Present != 1 {
		t.Errorf("present = %d, want 1 — a human's own answer does not expire", got.Present)
	}
}

// An empty string is a row, not an answer. A field written blank must not count
// toward a completeness figure that decides whether we may judge at all.
func TestABlankValueIsNotAPopulatedInput(t *testing.T) {
	in := Input{
		OrganizationID: ids.NewV7().String(),
		ProfileFields: []crmcontracts.CompanyProfileField{
			machineField("offer_summary", "   ", assessedAt),
		},
	}

	if got := Completeness(in, assessedAt); got.Present != 0 {
		t.Errorf("present = %d, want 0 — the row holds only whitespace", got.Present)
	}
}

// The floor is a proportion, so the two gates it separates must land on the
// right sides of it for the spec's own examples. This pins the boundary
// behaviour rather than the constant, so a re-parameterized floor that still
// satisfies the spec keeps passing and one that breaks an example does not.
func TestTheAbstentionFloorMatchesTheSpecsWorkedExamples(t *testing.T) {
	four := Completeness(fourOfSeven(), assessedAt)
	if !aboveFloor(four) {
		t.Errorf("four of seven abstained; the worked example judges it normally")
	}

	twoOfSeven := crmcontracts.DataCompleteness{Present: 2, Expected: 7}
	if aboveFloor(twoOfSeven) {
		t.Error("two of seven cleared the floor; DOSS-SEED-2 expects it to abstain")
	}
}

// An `unknown` band must never arrive bare, whatever produced it.
//
// The reader cannot distinguish "we could not tell" from "a poor fit" without
// a reason beside it, and those are opposite conclusions. Above the floor
// nothing is missing, so the honest reason is not a data-gathering step — it is
// that nothing is configured to judge with.
func TestAnAbstentionWithNothingMissingStillSaysWhyItAbstained(t *testing.T) {
	complete := fourOfSeven()
	fresh := assessedAt.Add(-24 * time.Hour)
	complete.ProfileFields = append(complete.ProfileFields,
		machineField("buying_center", "Head of Operations", fresh),
		machineField("buying_intents", "Cutting peak demand charges", fresh))
	complete.Facts = append(complete.Facts, machineFact("technology", "SAP S/4HANA", fresh))

	got := Assess(complete, crmcontracts.GrowthFitBandUnknown, true, AbstainedNoWriter, assessedAt)

	if got.Completeness.Present != got.Completeness.Expected {
		t.Fatalf("present/expected = %d/%d, want a complete company for this case",
			got.Completeness.Present, got.Completeness.Expected)
	}
	if got.Band != crmcontracts.GrowthFitBandUnknown {
		t.Errorf("band = %q, want unknown", got.Band)
	}
	if got.NextStep == "" {
		t.Error("a bare unknown: the reader cannot tell 'we could not judge' from 'a poor fit'")
	}
}

// An assembly that wants nothing has no proportion to compare, and must not
// divide by zero or judge a company it read nothing about.
func TestAnEmptyRequiredSetAbstainsRatherThanDividingByZero(t *testing.T) {
	if aboveFloor(crmcontracts.DataCompleteness{Present: 0, Expected: 0}) {
		t.Error("an assembly wanting no inputs claimed to be complete enough to judge")
	}
}

// The live production path: no writer in this tree sets `retrieved_at`, so
// every machine-read value arrives with it nil and freshness is measured from
// `updated_at`. Every other fixture here sets RetrievedAt, which means the
// branch production actually takes had no test at all.
func TestAValueWithNoRecordedReadTimeAgesOnWhenItWasLastWritten(t *testing.T) {
	for name, tc := range map[string]struct {
		writtenAgo  time.Duration
		wantPresent int
	}{
		"written yesterday counts":        {24 * time.Hour, 1},
		"written forty days ago does not": {40 * 24 * time.Hour, 0},
	} {
		t.Run(name, func(t *testing.T) {
			in := Input{
				OrganizationID: ids.NewV7().String(),
				ProfileFields: []crmcontracts.CompanyProfileField{{
					Id:        rowID(),
					Field:     crmcontracts.CompanyProfileFieldFieldOfferSummary,
					Value:     "Load-shifting software",
					Source:    crmcontracts.CompanyProfileFieldSourceSiteRead,
					UpdatedAt: assessedAt.Add(-tc.writtenAgo),
					// RetrievedAt deliberately nil — the shape every row in the
					// database actually has today.
				}},
			}

			if got := Completeness(in, assessedAt); got.Present != tc.wantPresent {
				t.Errorf("present = %d, want %d", got.Present, tc.wantPresent)
			}
		})
	}
}

// The completeness figure can change with no write at all — a value simply
// ages out. A cache keyed only on the inputs would serve the band resting on
// it forever, so the assessment carries the moment it stops being true.
func TestAnAssessmentKnowsWhenItsOwnCountingExpires(t *testing.T) {
	got := Assess(fourOfSeven(), crmcontracts.GrowthFitBandStrong, true, AbstainedNoWriter, assessedAt)

	if got.StaleAt.IsZero() {
		t.Fatal("no expiry: nothing would ever re-count these machine-read values")
	}
	// The four counted values were all read a day ago, so the figure holds
	// until the freshness window closes on them.
	want := assessedAt.Add(-24 * time.Hour).Add(freshness)
	if !got.StaleAt.Equal(want) {
		t.Errorf("stale at %v, want %v — the earliest expiry among the counted inputs", got.StaleAt, want)
	}
}

// A company held entirely on human-entered values has nothing that ages, and
// must not be given an expiry that would re-assess it forever on a clock.
func TestAnAssessmentOverHumanValuesAloneNeverExpiresOnTheClock(t *testing.T) {
	in := Input{
		OrganizationID: ids.NewV7().String(),
		ProfileFields: []crmcontracts.CompanyProfileField{{
			Id:        rowID(),
			Field:     crmcontracts.CompanyProfileFieldFieldOfferSummary,
			Value:     "Load-shifting software",
			Source:    crmcontracts.CompanyProfileFieldSourceHuman,
			UpdatedAt: assessedAt.Add(-5 * 365 * 24 * time.Hour),
		}},
	}

	if got := Assess(in, crmcontracts.GrowthFitBandWeak, true, AbstainedNoWriter, assessedAt); !got.StaleAt.IsZero() {
		t.Errorf("stale at %v, want never — a person's own answer does not age", got.StaleAt)
	}
}

// A band this contract does not know is not a judgment. Letting one through
// would slip past the cap, which compares by rank and ranks an unknown at zero.
func TestABandThisContractDoesNotKnowIsNotTreatedAsAJudgment(t *testing.T) {
	got := Assess(fourOfSeven(), crmcontracts.GrowthFitBand("excellent"), false, AbstainedNoWriter, assessedAt)

	if got.Band != crmcontracts.GrowthFitBandUnknown {
		t.Errorf("band = %q, want unknown — %q is not in the contract's vocabulary", got.Band, "excellent")
	}
}
