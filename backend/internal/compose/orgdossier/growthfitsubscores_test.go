// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package orgdossier

import (
	"os"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/claims"
)

// The band, taken apart (DOSS-AC-17..20, ADR-0095/A146).
//
// A sub-score is a claim like any other on this surface, so it passes the same
// grounding filter: a number a reader cannot check is the unexplainable score
// this whole model replaced.

func knownRecord() (map[claims.Evidence]bool, claims.Evidence) {
	cited := claims.Evidence{EntityType: "fact", EntityID: "f-1"}
	return map[claims.Evidence]bool{cited: true}, cited
}

func TestASubScoreCitingNothingTheAssemblyKnowsIsDropped(t *testing.T) {
	known, cited := knownRecord()

	kept := keepSubScores([]GrowthFitSubScore{
		{Dimension: "industry_fit", Score: 90, Reason: "Grounded.", Evidence: []claims.Evidence{cited}},
		{
			Dimension: "access",
			Score:     80,
			Reason:    "Invented.",
			Evidence:  []claims.Evidence{{EntityType: "fact", EntityID: "nobody-knows-this"}},
		},
	}, known)

	// The grounded one stands: one bad dimension does not discredit the ones
	// that cited real records.
	if len(kept) != 1 || kept[0].Dimension != "industry_fit" {
		t.Fatalf("kept = %+v, want only the grounded industry_fit", kept)
	}
}

func TestASubScoreOutsideTheFourDimensionsIsDropped(t *testing.T) {
	known, cited := knownRecord()

	kept := keepSubScores([]GrowthFitSubScore{
		{Dimension: "vibes", Score: 99, Reason: "Grounded.", Evidence: []claims.Evidence{cited}},
	}, known)

	// A fifth dimension is a question nobody asked, and a surface that labels
	// and orders four cannot render it.
	if len(kept) != 0 {
		t.Fatalf("kept = %+v, want none — the dimension enum is closed", kept)
	}
}

func TestASubScoreOffTheScaleIsDropped(t *testing.T) {
	known, cited := knownRecord()

	for _, score := range []int{-1, 101} {
		kept := keepSubScores([]GrowthFitSubScore{
			{Dimension: "access", Score: score, Reason: "Grounded.", Evidence: []claims.Evidence{cited}},
		}, known)
		if len(kept) != 0 {
			t.Fatalf("score %d survived; a value off the scale is not the scale it claims to be", score)
		}
	}
}

// The reason travels WITH the number. A bar with no sentence is exactly the
// unexplainable score this model was built to replace.
func TestASubScoreWithNoReasonIsDropped(t *testing.T) {
	known, cited := knownRecord()

	kept := keepSubScores([]GrowthFitSubScore{
		{Dimension: "company_size", Score: 70, Reason: "   ", Evidence: []claims.Evidence{cited}},
	}, known)

	if len(kept) != 0 {
		t.Fatalf("kept = %+v, want none — a score with no sentence behind it is not checkable", kept)
	}
}

// A surface renders four rows. A second score for one dimension would render
// as a fifth, or silently replace a reading the reader already has.
func TestARepeatedDimensionKeepsTheFirst(t *testing.T) {
	known, cited := knownRecord()

	kept := keepSubScores([]GrowthFitSubScore{
		{Dimension: "access", Score: 80, Reason: "First.", Evidence: []claims.Evidence{cited}},
		{Dimension: "access", Score: 20, Reason: "Second.", Evidence: []claims.Evidence{cited}},
	}, known)

	if len(kept) != 1 || kept[0].Score != 80 {
		t.Fatalf("kept = %+v, want one access at 80", kept)
	}
}

func TestAllFourDimensionsSurviveTogether(t *testing.T) {
	known, cited := knownRecord()

	var in []GrowthFitSubScore
	for _, dimension := range []string{"industry_fit", "company_size", "transformation_need", "access"} {
		in = append(in, GrowthFitSubScore{
			Dimension: dimension,
			Score:     75,
			Reason:    "Grounded.",
			Evidence:  []claims.Evidence{cited},
		})
	}

	if kept := keepSubScores(in, known); len(kept) != 4 {
		t.Fatalf("kept %d of 4 — the four the assessment decomposes into must all be renderable", len(kept))
	}
}

// DOSS-AC-18: below the abstention floor the sub-scores go with everything
// else. Serving "not enough evidence to judge" beside four confident bars would
// read as a band the surface is merely too shy to state — and a dimension
// scored 0 is a claim about the company where an absent one is a fact about
// the reading.
//
// This is structural rather than a filter: the assessment attaches the model's
// claims only after the floor has been cleared. The test pins the structure,
// because the failure mode is silent.
func TestSubScoresAreWithheldBelowTheAbstentionFloor(t *testing.T) {
	source, err := os.ReadFile("growthfitwrite.go")
	if err != nil {
		t.Fatalf("read the writer: %v", err)
	}
	body := string(source)

	abstains := strings.Index(body, "if out.Band == crmcontracts.GrowthFitBandUnknown {")
	attaches := strings.Index(body, "out.Claims = kept")
	if abstains < 0 || attaches < 0 {
		t.Fatalf("the abstention guard (%d) or the claim attachment (%d) is no longer spelled that way", abstains, attaches)
	}
	if abstains > attaches {
		t.Fatal("claims are attached BEFORE the abstention guard — an unknown band would ship with the sub-scores that justified a judgment it declined to make")
	}
}
