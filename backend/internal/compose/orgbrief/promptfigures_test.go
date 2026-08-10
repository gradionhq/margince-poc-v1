// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package orgbrief

// What the model is actually shown about money and about tasks.
//
// Both defects these cover were invisible to every existing test, because every
// existing test asserted the PROSE the brief produces and this is about the
// JSON it is produced from. A number the model misreads and a task state the
// model has to guess both look like model failures from the outside — the
// account brief said a 180,000 EUR deal was worth eighteen million, and said
// open tasks had been completed, on a page whose own cards said otherwise.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// The prompt carries a figure a reader can read, in the currency's own scale.
func TestTheDealAmountReachesTheModelAsAMajorUnitFigure(t *testing.T) {
	for _, tc := range []struct {
		name     string
		minor    int64
		currency string
		want     string
		reject   string
	}{
		// The issue's own case: 180,000.00 EUR read as eighteen million.
		{"a two-decimal currency", 18_000_000, "EUR", `"amount":"180000.00"`, "18000000"},
		// The other direction, and the one `/100` gets wrong: yen has no minor
		// unit, so the integer IS the figure and dividing would understate it
		// a hundredfold.
		{"a zero-decimal currency", 18_000_000, "JPY", `"amount":"18000000"`, "180000.00"},
		// Three digits, the standard's other exception.
		{"a three-decimal currency", 18_000_000, "KWD", `"amount":"18000.000"`, "180000.00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := json.Marshal(DealIn{
				ID: "d-1", Name: "Fleet retrofit", AmountMinor: tc.minor, Currency: tc.currency,
			})
			if err != nil {
				t.Fatalf("encoding the deal: %v", err)
			}
			got := string(encoded)
			if !strings.Contains(got, tc.want) {
				t.Errorf("the prompt carries %s, want %s — the model reads this figure as the "+
					"amount and writes it into customer-facing prose", got, tc.want)
			}
			if strings.Contains(got, tc.reject) {
				t.Errorf("the prompt still carries %q in %s, which is the same money at the "+
					"wrong scale", tc.reject, got)
			}
		})
	}
}

// An amount with no currency has no scale, so it is not shown at all: a figure
// printed without its code is a number whose scale the reader has to guess,
// which is the defect rather than a lesser form of it.
func TestAnAmountWithNoCurrencyIsNotShownAtAll(t *testing.T) {
	encoded, err := json.Marshal(DealIn{ID: "d-1", Name: "Unpriced", AmountMinor: 18_000_000})
	if err != nil {
		t.Fatalf("encoding the deal: %v", err)
	}
	if strings.Contains(string(encoded), "amount") {
		t.Errorf("the prompt carries an amount with no currency: %s", encoded)
	}
}

// A deal deliberately priced at nothing is not a deal nobody has priced, and the
// prompt has to be able to tell them apart. Nothing forbids a zero-priced deal —
// the paired-nullness CHECK admits it — so suppressing the figure would make one
// read exactly like the other in prose.
func TestAZeroPricedDealStillCarriesItsAmount(t *testing.T) {
	encoded, err := json.Marshal(DealIn{ID: "d-1", Name: "Pilot", AmountMinor: 0, Currency: "EUR"})
	if err != nil {
		t.Fatalf("encoding the deal: %v", err)
	}
	if !strings.Contains(string(encoded), `"amount":"0.00"`) {
		t.Errorf("a zero-priced deal reaches the model as %s with no amount, which reads as an "+
			"unpriced one", encoded)
	}
}

// The won-to-date total is the same defect on a second field, and the issue
// names only the first.
func TestTheWonLifetimeTotalReachesTheModelAsAMajorUnitFigure(t *testing.T) {
	encoded, err := json.Marshal(Input{Name: "Acme", WonLifetime: 1_200_000, WonCurrency: "EUR"})
	if err != nil {
		t.Fatalf("encoding the input: %v", err)
	}
	if !strings.Contains(string(encoded), `"won_lifetime":"12000.00"`) {
		t.Errorf("the won total reaches the model as %s, want 12000.00 — the same minor-unit "+
			"misreading as the open deals, on the figure that describes the whole account", encoded)
	}
	if strings.Contains(string(encoded), "1200000") {
		t.Errorf("the won total still carries its minor-unit integer: %s", encoded)
	}
}

// A task on the timeline says whether it is finished, END TO END from the 360.
//
// Through FromView and into the bytes the model receives, not by setting the
// field this test is about: the mapping from the contract's Activity to the
// prompt's shape is exactly what was missing, so a test that builds ActIn
// itself would stay green while the mapping that reintroduces #592 rots
// underneath it.
//
// It reached the model twice — once under open_tasks and once here, as a
// past-dated row with no state — and nothing linked the two shapes or said the
// second was still outstanding. The model did the reasonable thing with a dated
// entry and reported the account's open tasks as completed, above a card
// showing one of them overdue.
func TestATaskOnTheTimelineCarriesWhetherItIsDone(t *testing.T) {
	open, done := false, true
	canceled := crmcontracts.ActivityMeetingStatusCanceled
	subject := "Contract walkthrough"
	view := crmcontracts.Organization360{
		Organization: crmcontracts.Organization{DisplayName: "Nordwind AG"},
		Activities: &crmcontracts.ActivityListResponse{Data: []crmcontracts.Activity{
			{
				Id: openapi_types.UUID(ids.NewV7()), Kind: crmcontracts.ActivityKindTask,
				Subject: &subject, IsDone: &open, OccurredAt: time.Now().UTC(),
			},
			{
				Id: openapi_types.UUID(ids.NewV7()), Kind: crmcontracts.ActivityKindTask,
				Subject: &subject, IsDone: &done, OccurredAt: time.Now().UTC(),
			},
			// A kind that cannot BE finished says nothing rather than false: a
			// call happened, and answering whether it is "done" invents a state
			// the record does not have.
			{
				Id: openapi_types.UUID(ids.NewV7()), Kind: crmcontracts.ActivityKindCall,
				Subject: &subject, OccurredAt: time.Now().UTC(),
			},
			// A meeting CAN be finished, in its own vocabulary — and it is
			// dated at its SLOT, so a cancelled or still-upcoming one arrives
			// on the timeline looking exactly like a past event. Same
			// mechanism as the task, on the kind whose dates run forward.
			{
				Id: openapi_types.UUID(ids.NewV7()), Kind: crmcontracts.ActivityKindMeeting,
				Subject: &subject, MeetingStatus: &canceled, OccurredAt: time.Now().UTC(),
			},
		}},
	}

	encoded, err := json.Marshal(FromView(view))
	if err != nil {
		t.Fatalf("encoding the assembled input: %v", err)
	}
	payload := string(encoded)
	for _, want := range []string{`"done":false`, `"done":true`} {
		if !strings.Contains(payload, want) {
			t.Errorf("the prompt does not carry %s — a task on the timeline with no state is one "+
				"the model reads as finished because the row is dated: %s", want, payload)
		}
	}
	if strings.Count(payload, `"done"`) != 2 {
		t.Errorf("the prompt carries %d done keys for two tasks, one call and one meeting, want 2 "+
			"— a call is neither outstanding nor complete, and a meeting has its own vocabulary: %s",
			strings.Count(payload, `"done"`), payload)
	}
	if !strings.Contains(payload, `"status":"canceled"`) {
		t.Errorf("the prompt does not say the meeting was cancelled — a meeting is dated at its "+
			"SLOT, so one that never happened arrives looking exactly like one that did: %s", payload)
	}
}
