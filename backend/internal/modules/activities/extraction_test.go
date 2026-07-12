// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

import (
	"reflect"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/extraction"
)

// partitionExtraction is the pure evidence-or-omit split (RD-T10): every
// grounded field carries its evidence, every omitted field carries a reason
// — never a guessed value — and both wire slices stay non-nil even when
// empty (the contract's `{fields: [], omitted: []}`, never `null`).
func TestPartitionExtractionSplitsGroundedFromOmitted(t *testing.T) {
	fields := []extraction.ExtractedField{
		{Field: "amount_minor", Value: "150000", SourceQuote: "Total: $1,500.00", PageOrSection: "p.1", Confidence: "high"},
		{Field: "currency", Value: "USD", SourceQuote: "$1,500.00", PageOrSection: "p.1", Confidence: "medium"},
		{Field: "expected_close_date", Omitted: true, OmittedReason: "not_stated_in_file"},
	}

	got := partitionExtraction(fields)

	want := crmcontracts.AttachmentExtraction{
		Fields: []crmcontracts.ExtractedField{
			{Field: "amount_minor", Value: "150000", SourceQuote: "Total: $1,500.00", PageOrSection: "p.1", Confidence: "high"},
			{Field: "currency", Value: "USD", SourceQuote: "$1,500.00", PageOrSection: "p.1", Confidence: "medium"},
		},
		Omitted: []crmcontracts.OmittedExtractionField{
			{Field: "expected_close_date", Reason: crmcontracts.OmittedExtractionFieldReason("not_stated_in_file")},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("partitionExtraction = %+v, want %+v", got, want)
	}
}

func TestPartitionExtractionOfNoFieldsIsEmptyNotNil(t *testing.T) {
	got := partitionExtraction(nil)
	if got.Fields == nil || len(got.Fields) != 0 {
		t.Errorf("Fields = %#v, want a non-nil empty slice", got.Fields)
	}
	if got.Omitted == nil || len(got.Omitted) != 0 {
		t.Errorf("Omitted = %#v, want a non-nil empty slice", got.Omitted)
	}
}

// The production default is honestly empty, never a 501: a Handlers value
// that never called WithExtractor still answers a valid (empty) extraction.
func TestHandlersExtractorOrNoOpFallsBackWhenUnwired(t *testing.T) {
	h := Handlers{}
	extractor := h.extractorOrNoOp()
	if _, ok := extractor.(extraction.NoOpExtractor); !ok {
		t.Fatalf("extractorOrNoOp() = %T, want extraction.NoOpExtractor", extractor)
	}
	fields, err := extractor.Extract(t.Context(), "any-id")
	if err != nil {
		t.Fatalf("NoOpExtractor.Extract: %v", err)
	}
	if len(fields) != 0 {
		t.Errorf("NoOpExtractor.Extract returned %d fields, want 0", len(fields))
	}
}

// WithExtractor wires the given seam in place of the fallback (mirrors
// WithBlobstore's shape: a copy carries the option, the base value is
// unchanged).
func TestWithExtractorWiresTheGivenSeam(t *testing.T) {
	fx := extraction.FixtureExtractor{Fields: map[string][]extraction.ExtractedField{
		"att-1": {{Field: "name", Value: "Acme Corp", SourceQuote: "Acme Corp", PageOrSection: "p.1", Confidence: "high"}},
	}}
	base := Handlers{}
	wired := base.WithExtractor(fx)

	got, err := wired.extractorOrNoOp().Extract(t.Context(), "att-1")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(got) != 1 || got[0].Value != "Acme Corp" {
		t.Fatalf("Extract(att-1) = %+v, want the fixture's seeded field", got)
	}

	if _, ok := base.extractorOrNoOp().(extraction.NoOpExtractor); !ok {
		t.Error("WithExtractor mutated the base Handlers value — it must return a copy")
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
