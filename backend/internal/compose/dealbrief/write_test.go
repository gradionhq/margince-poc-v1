// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dealbrief

import (
	"strings"
	"testing"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestTheBriefStatesWhatIsOnTheRecordAndCitesIt(t *testing.T) {
	now := time.Date(2026, 8, 23, 9, 0, 0, 0, time.UTC)
	amount := int64(1_200_000)
	eur := "EUR"
	closeDay := openapi_types.Date{Time: now.Add(10 * 24 * time.Hour)}
	subject := "Re: MSA"
	meeting := "Kick-off"
	booked := crmcontracts.ActivityMeetingStatusBooked
	dealID := openapi_types.UUID(ids.NewV7())
	releases := 2
	reviewer := "Laura Kern"
	f := facts{
		now: now,
		deal: crmcontracts.Deal{
			Id: dealID, Name: "Acme rollout", Status: crmcontracts.DealStatusOpen,
			AmountMinor: &amount, Currency: &eur, ExpectedCloseDate: &closeDay,
		},
		health: &deals.DealHealth{Health: 0.8, Evidence: deals.DealHealthEvidence{DaysInStage: 3, ExpectedDaysInStage: 14}},
		timeline: []crmcontracts.Activity{
			{Id: openapi_types.UUID(ids.NewV7()), Kind: crmcontracts.ActivityKindMeeting, Subject: &meeting, MeetingStatus: &booked, OccurredAt: now.Add(48 * time.Hour)},
			{Id: openapi_types.UUID(ids.NewV7()), Kind: crmcontracts.ActivityKindEmail, Subject: &subject, OccurredAt: now.Add(-3 * 24 * time.Hour)},
		},
		openTasks: []activities.OpenTask{{ID: ids.NewV7(), Subject: "Send the SOW"}},
		room:      &crmcontracts.DealRoom{Title: "Acme room", State: "live", ReleaseCount: &releases},
		threads: []crmcontracts.DealRoomThread{{State: "open", Comments: []crmcontracts.DealRoomComment{
			{Author: crmcontracts.DealRoomAuthor{Side: "buyer"}},
		}}},
		decisions: []crmcontracts.DealRoomDecision{{Kind: "confirm_version", ParticipantName: &reviewer, CreatedAt: now.Add(-24 * time.Hour)}},
	}
	sections := write(f)
	got := map[crmcontracts.DealBriefSectionKind]string{}
	for _, s := range sections {
		var texts []string
		for _, line := range s.Sentences {
			if len(line.Evidence) == 0 {
				t.Errorf("%s: %q cites nothing", s.Kind, line.Text)
			}
			texts = append(texts, line.Text)
		}
		got[s.Kind] = strings.Join(texts, " ")
	}
	want := map[crmcontracts.DealBriefSectionKind][]string{
		sectionStanding: {"Acme rollout is open at 12000.00 EUR", "expected to close 2 Sep 2026", "Health reads 80 of 100", "3 days in the current stage"},
		sectionActivity: {"Last activity: Re: MSA, 3 days ago", "Next meeting: Kick-off"},
		sectionOpen:     {"1 open task, starting with \"Send the SOW\""},
		sectionRoom:     {"\"Acme room\" is live, published 2 time(s)", "1 thread from the buyer waiting", "Laura Kern confirmed a document, yesterday"},
	}
	for kind, parts := range want {
		for _, part := range parts {
			if !strings.Contains(got[kind], part) {
				t.Errorf("%s = %q, want it to say %q", kind, got[kind], part)
			}
		}
	}
}

func TestAnEmptyDealHasOnlyItsStanding(t *testing.T) {
	f := facts{
		now:    time.Date(2026, 8, 23, 9, 0, 0, 0, time.UTC),
		deal:   crmcontracts.Deal{Id: openapi_types.UUID(ids.NewV7()), Name: "Bare", Status: crmcontracts.DealStatusOpen},
		health: &deals.DealHealth{Health: 0.5, Evidence: deals.DealHealthEvidence{ExpectedDaysInStage: 14}},
	}
	sections := write(f)
	if len(sections) != 1 || sections[0].Kind != sectionStanding {
		t.Fatalf("sections = %+v, want only standing", sections)
	}
}
