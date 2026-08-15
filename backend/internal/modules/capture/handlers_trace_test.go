// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// The transport's mapping, which is where an absent value and an empty one stop
// being the same thing. Everything asserted here is a distinction a client
// renders differently, so getting it wrong shows a reader a fact the server
// never stated.

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestTheFunnelReportsOnlyTheOutcomesItCounted(t *testing.T) {
	got := traceFunnelResponse(map[string]int{"captured": 7, "internal": 0})

	if got.Captured == nil || *got.Captured != 7 {
		t.Errorf("captured = %v, want 7", got.Captured)
	}
	// Zero is a COUNT, not an absence: the pipeline ran and dropped nothing as
	// internal, which is a different fact from an outcome nobody measured.
	if got.Internal == nil || *got.Internal != 0 {
		t.Errorf("internal = %v, want a present zero", got.Internal)
	}
	if got.Deferred != nil {
		t.Errorf("deferred = %v, want absent — nothing counted it", got.Deferred)
	}
}

func TestAnUnknownOutcomeIsDroppedRatherThanGuessed(t *testing.T) {
	// A row written by a newer binary against an older reader. Dropping it
	// under-reports; folding it into a neighbour would misreport, which is
	// worse on a surface whose whole job is to say what happened.
	got := traceFunnelResponse(map[string]int{"captured": 2, "teleported": 9})
	if got.Captured == nil || *got.Captured != 2 {
		t.Errorf("captured = %v, want 2", got.Captured)
	}
}

func TestAnEntryDistinguishesAbsentFromEmpty(t *testing.T) {
	activityID := ids.NewV7()
	resolvedAt := time.Date(2026, 8, 15, 9, 30, 0, 0, time.UTC)
	got := traceEntryResponse(TraceRow{
		ID:         ids.NewV7(),
		Connector:  "gmail",
		Outcome:    string(TraceDeferred),
		Reason:     "",
		ActivityID: &activityID,
		Resolution: &TraceResolution{Status: "real", Kind: "person", ResolvedAt: &resolvedAt},
		OccurredAt: time.Now().UTC(),
	})

	// "" and "there is none" are different answers, and a client rendering a
	// reason line must be able to tell them apart.
	if got.Reason != nil {
		t.Errorf("reason = %q, want null for a row with none", *got.Reason)
	}
	if got.Counterparty != nil || got.Subject != nil {
		t.Error("payload fields are set on a row that carried none")
	}
	if got.ActivityId == nil {
		t.Error("activity_id = nil, want the link the row carried")
	}
	if got.Resolution == nil || got.Resolution.Status != "real" {
		t.Errorf("resolution = %v, want the ledger's answer", got.Resolution)
	}
}

func TestAnEntryWithNoOpenQuestionCarriesNoResolution(t *testing.T) {
	got := traceEntryResponse(TraceRow{
		ID: ids.NewV7(), Connector: "imap", Outcome: string(TraceInternal),
		OccurredAt: time.Now().UTC(),
	})
	if got.Resolution != nil {
		t.Errorf("resolution = %v, want absent — a dropped message asked nothing", got.Resolution)
	}
	if got.ActivityId != nil {
		t.Errorf("activity_id = %v, want absent — nothing was written to link to", got.ActivityId)
	}
}

func TestTheResponseReportsThePostureRatherThanInferringIt(t *testing.T) {
	// Inferred from the rows, a window in which every message happened to carry
	// no subject would look exactly like an operator who never enabled capture.
	got := traceResponse(TraceWindow{Funnel: map[string]int{}}, true)
	if !got.PayloadCaptureEnabled {
		t.Error("payload_capture_enabled = false, want the posture the deployment set")
	}
	if got.WindowHours != TraceWindowHours {
		t.Errorf("window_hours = %d, want %d", got.WindowHours, TraceWindowHours)
	}
	if got.Data == nil {
		t.Error("data = nil, want an empty array — a client should not have to guard a null")
	}
}

func TestAnUncomposedTraceSurfaceAnswersUnavailable(t *testing.T) {
	// A role that composed no capture pipeline. The operations are declared in
	// the contract, so the honest answer is that this deployment cannot serve
	// them — not a nil dereference, and not an empty page that reads as "you
	// have no capture activity".
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/capture/activity", nil)
	TraceHandlers{}.ListMyCaptureActivity(w, r, crmcontracts.ListMyCaptureActivityParams{})

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
}
