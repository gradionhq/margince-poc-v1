// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package org360

import (
	"os"
	"strings"
	"testing"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

// The health card's named dimensions (PO-AC-N-10..12, ADR-0095/A146).
//
// The rule under all of them: a dimension that cannot be READ is absent, never
// rated. Absence is a fact about the reading; a rating is a claim about the
// account, and the two must not render alike.

func ptrInt(v int) *int    { return &v }
func ptrBool(v bool) *bool { return &v }

// commercialStrip builds the state strip's commercial half — the only part
// these ratings read. Built by assigning fields rather than by a composite
// literal: the generated type is an anonymous struct, so restating its shape
// here would be a copy that drifts the moment the contract gains a field.
func commercialStrip(open, stalled int) *crmcontracts.Organization360StateStrip {
	strip := &crmcontracts.Organization360StateStrip{}
	strip.Commercial = new(struct {
		BaseCurrency          *string             `json:"base_currency,omitempty"`
		ConvertedCount        int                 `json:"converted_count"`
		FxAsOf                *openapi_types.Date `json:"fx_as_of,omitempty"`
		NextCloseOn           *openapi_types.Date `json:"next_close_on,omitempty"`
		OpenCount             int                 `json:"open_count"`
		OpenPipelineMinorBase *int                `json:"open_pipeline_minor_base,omitempty"`
		PricedCount           int                 `json:"priced_count"`
		StalledCount          int                 `json:"stalled_count"`
	})
	strip.Commercial.OpenCount = open
	strip.Commercial.StalledCount = stalled
	return strip
}

func TestRelationshipIsAbsentOnAnAccountNobodyHasReached(t *testing.T) {
	health := crmcontracts.Organization360Health{ActiveContacts: ptrInt(0)}
	rateHealthDimensions(&health, nil)

	// Not "at risk": an unstarted relationship is not a failing one, and rating
	// it would put a verdict on something that has not begun.
	if health.Relationship != nil {
		t.Fatalf("relationship = %+v, want absent on an account with no contact", health.Relationship)
	}
}

func TestRelationshipReadsWhetherBothSidesAreTalking(t *testing.T) {
	cases := []struct {
		name           string
		activeContacts int
		daysSince      *int
		singleThreaded bool
		want           crmcontracts.HealthDimensionRating
		reasonHas      string
	}{
		{
			name:           "they have never written",
			activeContacts: 2,
			daysSince:      nil,
			want:           crmcontracts.HealthDimensionRatingAtRisk,
			reasonHas:      "never written",
		},
		{
			name:           "quiet past the threshold",
			activeContacts: 2,
			daysSince:      ptrInt(healthQuietDays + 1),
			want:           crmcontracts.HealthDimensionRatingAtRisk,
			reasonHas:      "No reply",
		},
		{
			name:           "in contact, but one person carries it",
			activeContacts: 1,
			daysSince:      ptrInt(3),
			singleThreaded: true,
			want:           crmcontracts.HealthDimensionRatingGood,
			reasonHas:      "one person",
		},
		{
			name:           "several people, recently",
			activeContacts: 3,
			daysSince:      ptrInt(3),
			want:           crmcontracts.HealthDimensionRatingStrong,
			reasonHas:      "in contact",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			health := crmcontracts.Organization360Health{
				ActiveContacts:       ptrInt(tc.activeContacts),
				DaysSinceLastInbound: tc.daysSince,
				SingleThreaded:       ptrBool(tc.singleThreaded),
			}
			rateHealthDimensions(&health, nil)

			if health.Relationship == nil {
				t.Fatal("relationship is absent on an account with contacts")
			}
			if health.Relationship.Rating != tc.want {
				t.Fatalf("rating = %q, want %q", health.Relationship.Rating, tc.want)
			}
			if !strings.Contains(health.Relationship.Reason, tc.reasonHas) {
				t.Fatalf("reason = %q, want it to name %q — a rating with no sentence behind it is the unexplainable score this model replaced",
					health.Relationship.Reason, tc.reasonHas)
			}
		})
	}
}

func TestCommercialIsAbsentWhenTheReaderHasNoDealGrant(t *testing.T) {
	health := crmcontracts.Organization360Health{ActiveContacts: ptrInt(2)}
	// A nil commercial half is the strip saying the READER cannot see deals.
	rateHealthDimensions(&health, &crmcontracts.Organization360StateStrip{})

	if health.Commercial != nil {
		t.Fatalf("commercial = %+v, want absent — a withheld section is not a claim about the account", health.Commercial)
	}
}

func TestCommercialReadsWhetherWorkIsMoving(t *testing.T) {
	cases := []struct {
		name      string
		open      int
		stalled   int
		want      crmcontracts.HealthDimensionRating
		reasonHas string
	}{
		{"nothing open", 0, 0, crmcontracts.HealthDimensionRatingAtRisk, "Nothing open"},
		{"everything stalled", 2, 2, crmcontracts.HealthDimensionRatingAtRisk, "All 2"},
		{"some stalled", 3, 1, crmcontracts.HealthDimensionRatingGood, "1 of 3"},
		{"all moving", 2, 0, crmcontracts.HealthDimensionRatingStrong, "none stalled"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			health := crmcontracts.Organization360Health{}
			rateHealthDimensions(&health, commercialStrip(tc.open, tc.stalled))

			if health.Commercial == nil {
				t.Fatal("commercial is absent where the strip carried a reading")
			}
			if health.Commercial.Rating != tc.want {
				t.Fatalf("rating = %q, want %q", health.Commercial.Rating, tc.want)
			}
			if !strings.Contains(health.Commercial.Reason, tc.reasonHas) {
				t.Fatalf("reason = %q, want it to name %q", health.Commercial.Reason, tc.reasonHas)
			}
		})
	}
}

// readHealth reads the state strip, so the strip must be assembled FIRST.
//
// The section list is an ordered literal inside `sections`, so this reads the
// source: reordering the two would leave commercial silently absent on every
// account, which renders as "nothing open" rather than as a bug. A comment
// could be ignored; this cannot.
func TestHealthIsAssembledAfterTheStateStripItReads(t *testing.T) {
	raw, err := os.ReadFile("assemble.go")
	if err != nil {
		t.Fatalf("read the assembler: %v", err)
	}
	source := string(raw)
	strip := strings.Index(source, "{sectionStateStrip, a.readStateStrip}")
	health := strings.Index(source, "{sectionHealth, a.readHealth}")
	if strip < 0 || health < 0 {
		t.Fatalf("state strip (%d) or health (%d) is no longer in the section list under that name", strip, health)
	}
	if strip > health {
		t.Fatal("health is assembled BEFORE the state strip it reads — commercial would be absent on every account, and absent renders as \"nothing open\"")
	}
}
