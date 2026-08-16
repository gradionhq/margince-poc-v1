// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package pipelinetrace

// What reaches the wire.
//
// The wire is the LAST place the payload posture is enforced, and the place a
// stage the client has never heard of either survives or vanishes. Both are
// properties of this file rather than of the assembler, so both are asserted
// here rather than inferred from the rungs.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	trace "github.com/gradionhq/margince/backend/internal/shared/kernel/pipelinetrace"
)

func TestThePostureIsEnforcedAtTheWire(t *testing.T) {
	// The assembler already declines to copy content onto a rung the caller
	// does not own. This is the second of two gates, and the one a future rung
	// builder cannot bypass by forgetting the first.
	rung := Rung{
		Stage: trace.StageTierLadder, Status: trace.StatusDone,
		Counterparty: "dana@client.io", Subject: "Q3 pricing",
	}
	off := wireRung(rung, false)
	if off.Counterparty != nil || off.Subject != nil {
		t.Errorf("payload reached the wire with the posture off: %v / %v",
			off.Counterparty, off.Subject)
	}
	on := wireRung(rung, true)
	if on.Counterparty == nil || *on.Counterparty != "dana@client.io" {
		t.Errorf("counterparty = %v, want it carried under the payload posture", on.Counterparty)
	}
}

func TestAnEmptyPayloadFieldTravelsAsNullNotAsBlank(t *testing.T) {
	// A blank string on the wire reads as "we have this and it is empty",
	// which is a different claim from having none.
	got := wireRung(Rung{Stage: trace.StageTierLadder, Status: trace.StatusDone}, true)
	if got.Counterparty != nil || got.Subject != nil {
		t.Errorf("an absent payload rendered as %v / %v, want null",
			got.Counterparty, got.Subject)
	}
}

func TestEveryRungCarriesAServerRenderedFallback(t *testing.T) {
	// The growth seam. A client that does not recognise a stage renders from
	// `label`, and one that does not recognise a reason renders from
	// `reason_text` — so a stage added by a newer server must never arrive with
	// neither, or it vanishes at a member.
	got := wireRung(Rung{
		Stage:  "a_stage_from_the_future",
		Status: trace.StatusSkipped,
		Reason: "a_reason_from_the_future",
	}, false)
	if got.Label == nil || *got.Label == "" {
		t.Error("a rung reached the wire with no label to fall back to")
	}
	if got.ReasonText == nil || *got.ReasonText == "" {
		t.Error("a rung carried a reason with no text to fall back to")
	}
	// And the fallback must not be an empty-looking key: an unknown stage's
	// label is its own id, which at least tells a reader a step exists.
	if *got.Label != "a_stage_from_the_future" {
		t.Errorf("label = %q, want the stage id as the last resort", *got.Label)
	}
}

func TestARungWithNoReasonCarriesNoReasonText(t *testing.T) {
	got := wireRung(Rung{Stage: trace.StageTierLadder, Status: trace.StatusDone}, false)
	if got.Reason != nil || got.ReasonText != nil {
		t.Errorf("reason/%v text/%v travelled for a rung that has none", got.Reason, got.ReasonText)
	}
}

func TestTheLadderReportsTheRetentionItActuallyKeeps(t *testing.T) {
	// A client tells a member how long this is kept. Reporting anything but the
	// sweep's own number would promise a window the sweep does not honour.
	at := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	id := ids.NewV7()
	got := Handlers{}.wire(Ladder{
		ActivityID: &id, Connector: "telegram", PayloadsEnabled: true,
		Rungs: []Rung{{Stage: trace.StageTierLadder, Status: trace.StatusDone, At: &at}},
	})
	if got.RetentionHours != trace.RetentionHours {
		t.Errorf("retention_hours = %d, want %d", got.RetentionHours, trace.RetentionHours)
	}
	if !got.PayloadCaptureEnabled {
		t.Error("the posture did not reach the wire")
	}
	if got.ActivityId == nil || ids.UUID(*got.ActivityId) != id {
		t.Errorf("activity_id = %v, want %v", got.ActivityId, id)
	}
	if len(got.Stages) != 1 || got.Stages[0].At == nil || !got.Stages[0].At.Equal(at) {
		t.Errorf("the rung's timestamp did not survive the wire: %+v", got.Stages)
	}
}

func TestALadderWithNoActivityOmitsTheId(t *testing.T) {
	// An internal-only drop never produced one, and a hidden activity has had
	// it removed. Either way a null id must not become a zero uuid, which would
	// read as a record that exists.
	got := Handlers{}.wire(Ladder{Rungs: []Rung{}})
	if got.ActivityId != nil {
		t.Errorf("activity_id = %v, want null", got.ActivityId)
	}
	body, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshalling the ladder: %v", err)
	}
	if s := string(body); contains(s, `"activity_id":"00000000`) {
		t.Errorf("a null activity serialised as a zero uuid: %s", s)
	}
}

func TestAnUncomposedDeploymentSaysSoRatherThanPanicking(t *testing.T) {
	// The worker role composes no assembler. A nil dereference here would take
	// the process down; the honest answer is that this deployment does not
	// serve the surface.
	for _, call := range []func(Handlers, http.ResponseWriter, *http.Request){
		func(h Handlers, w http.ResponseWriter, r *http.Request) {
			h.ReadActivityPipelineTrace(w, r, crmcontracts.Id(ids.NewV7()))
		},
		func(h Handlers, w http.ResponseWriter, r *http.Request) {
			h.ReadCaptureTracePipeline(w, r, crmcontracts.Id(ids.NewV7()))
		},
	} {
		w := httptest.NewRecorder()
		call(Handlers{}, w, httptest.NewRequest(http.MethodGet, "/v1/x", nil))
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("status = %d, want %d", w.Code, http.StatusServiceUnavailable)
		}
	}
}

func TestNewHandlersCarriesItsAssembler(t *testing.T) {
	// The zero value answers 503 by design, so a constructor that dropped its
	// argument would make every deployment look uncomposed.
	if NewHandlers(&Assembler{}).assembler == nil {
		t.Error("NewHandlers discarded the assembler")
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
