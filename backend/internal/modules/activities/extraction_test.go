// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

import (
	"reflect"
	"testing"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/extraction"
)

// ExtractionReport is the pure evidence-or-omit split (RD-AC-N-3): every
// grounded field carries its evidence, every omitted field carries the reason
// it was omitted — never a guessed value — and both wire slices stay non-nil
// even when empty (the contract's `[]`, never `null`).
func TestExtractionReportSplitsGroundedFromOmitted(t *testing.T) {
	readID := ids.NewV7()
	created := time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)
	read := ExtractionRead{
		ID:        readID,
		Status:    ExtractionReadDone,
		CreatedAt: created,
		Fields: []extraction.ExtractedField{
			{Field: "amount_minor", Value: "150000", SourceQuote: "Total: $1,500.00", PageOrSection: "p.1", Confidence: "high"},
			{Field: "currency", Value: "USD", SourceQuote: "$1,500.00", PageOrSection: "p.1", Confidence: "medium"},
			{Field: "expected_close_date", Omitted: true, OmittedReason: "not_stated_in_file"},
		},
	}

	got := ExtractionReport(read)

	want := crmcontracts.AttachmentExtraction{
		Id:        openapi_types.UUID(readID),
		Status:    crmcontracts.AttachmentExtractionStatusDone,
		CreatedAt: created,
		Fields: []crmcontracts.ExtractedField{
			{Field: "amount_minor", Value: "150000", SourceQuote: "Total: $1,500.00", PageOrSection: "p.1", Confidence: "high"},
			{Field: "currency", Value: "USD", SourceQuote: "$1,500.00", PageOrSection: "p.1", Confidence: "medium"},
		},
		Omitted: []crmcontracts.OmittedExtractionField{
			{Field: "expected_close_date", Reason: crmcontracts.OmittedExtractionFieldReason("not_stated_in_file")},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ExtractionReport = %+v, want %+v", got, want)
	}
}

// The two omission reasons reach the wire distinctly. Folding them together
// would make the confidence floor invisible: every under-confident reading
// would present as a document that says nothing, which is a claim about the
// DOCUMENT made on the strength of a fact about the READING (RD-PARAM-N-6).
func TestExtractionReportKeepsTheTwoOmissionReasonsApart(t *testing.T) {
	got := ExtractionReport(ExtractionRead{
		Status: ExtractionReadDone,
		Fields: []extraction.ExtractedField{
			{Field: "currency", Omitted: true, OmittedReason: "not_stated_in_file"},
			{Field: "amount_minor", Omitted: true, OmittedReason: "not_confidently_stated"},
		},
	})

	reasons := map[string]string{}
	for _, o := range got.Omitted {
		reasons[o.Field] = string(o.Reason)
	}
	if reasons["currency"] != "not_stated_in_file" {
		t.Errorf("currency omitted as %q, want not_stated_in_file", reasons["currency"])
	}
	if reasons["amount_minor"] != "not_confidently_stated" {
		t.Errorf("amount_minor omitted as %q, want not_confidently_stated", reasons["amount_minor"])
	}
}

// A reading still in flight reports its status and carries no fields — which is
// a different answer from a finished reading that grounded none, and the wire
// has to be able to say so (RD-AC-N-2).
func TestExtractionReportOfALiveReadingIsEmptyNotNil(t *testing.T) {
	got := ExtractionReport(ExtractionRead{Status: ExtractionReadRunning})
	if got.Status != crmcontracts.AttachmentExtractionStatusRunning {
		t.Errorf("Status = %q, want running", got.Status)
	}
	if got.Fields == nil || len(got.Fields) != 0 {
		t.Errorf("Fields = %#v, want a non-nil empty slice", got.Fields)
	}
	if got.Omitted == nil || len(got.Omitted) != 0 {
		t.Errorf("Omitted = %#v, want a non-nil empty slice", got.Omitted)
	}
}

// Live is the one question a polling client actually asks: keep asking, or stop.
func TestExtractionReadLiveOnlyWhileItStillMoves(t *testing.T) {
	for status, live := range map[string]bool{
		ExtractionReadQueued:  true,
		ExtractionReadRunning: true,
		ExtractionReadDone:    false,
		ExtractionReadFailed:  false,
	} {
		if got := (ExtractionRead{Status: status}).Live(); got != live {
			t.Errorf("ExtractionRead{Status: %q}.Live() = %v, want %v", status, got, live)
		}
	}
}

// A finished reading that grounded nothing MUST say why, or its result cannot
// be told apart from a broken one — the distinction the whole asynchronous
// shape exists to preserve (RD-AC-N-2).
func TestFinishExtractionReadRefusesAnUnexplainedEmptyOutcome(t *testing.T) {
	store := &Store{}
	err := store.FinishExtractionRead(t.Context(), ids.NewV7(), ExtractionReadOutcome{
		Status: ExtractionReadDone,
	})
	if err == nil {
		t.Fatal("FinishExtractionRead accepted a done reading with no fields and no detail")
	}
}

// A reading whose only rows are omissions has grounded nothing, however many
// rows it carries — so it owes a detail exactly like an empty one. The count,
// not the length, is what decides.
func TestFinishExtractionReadRefusesAnUnexplainedAllOmittedOutcome(t *testing.T) {
	store := &Store{}
	err := store.FinishExtractionRead(t.Context(), ids.NewV7(), ExtractionReadOutcome{
		Status: ExtractionReadDone,
		Fields: []extraction.ExtractedField{
			{Field: "currency", Omitted: true, OmittedReason: "not_stated_in_file"},
		},
	})
	if err == nil {
		t.Fatal("FinishExtractionRead accepted an all-omitted reading with no detail")
	}
}

// Only the two terminal statuses close a reading. A worker that wrote `running`
// here would leave the row live forever with nothing coming for it.
func TestFinishExtractionReadRefusesANonTerminalStatus(t *testing.T) {
	store := &Store{}
	err := store.FinishExtractionRead(t.Context(), ids.NewV7(), ExtractionReadOutcome{
		Status: ExtractionReadRunning,
		Detail: "still going",
	})
	if err == nil {
		t.Fatal("FinishExtractionRead accepted a non-terminal status")
	}
}

// The claim interval is the worker's own lease and must be positive: a
// zero-or-negative one would treat every live reading as abandoned and let two
// workers read — and pay for — the same document at once.
func TestBeginExtractionReadRefusesANonPositiveReclaimInterval(t *testing.T) {
	store := &Store{}
	if _, err := store.BeginExtractionRead(t.Context(), ids.NewV7(), 0); err == nil {
		t.Fatal("BeginExtractionRead accepted a zero reclaim interval")
	}
}

// requestAccessLinks ties the courtesy note back to the parent only for the
// entity kinds activity_link actually carries a column for.
func TestRequestAccessLinksOnlyForLinkableEntityTypes(t *testing.T) {
	id := ids.NewV7()
	cases := map[crmcontracts.AttachmentEntityType]bool{
		"person":       true,
		"organization": true,
		"deal":         true,
		"activity":     false,
		"lead":         false,
	}
	for entityType, wantLinked := range cases {
		links := requestAccessLinks(entityType, id)
		if wantLinked && len(links) != 1 {
			t.Errorf("requestAccessLinks(%s) = %+v, want one link", entityType, links)
		}
		if !wantLinked && links != nil {
			t.Errorf("requestAccessLinks(%s) = %+v, want nil (not activity_link-able)", entityType, links)
		}
	}
}
