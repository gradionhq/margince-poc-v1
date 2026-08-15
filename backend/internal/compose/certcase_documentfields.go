// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The certification case for document_extract/fields.
//
// It certifies the shipped path: the request comes from documentExtractRequest
// and the reply is read by readDocumentFields, the same builder and the same
// grounding the engine uses. A case that rebuilt either would measure a copy,
// and a copy stays green through the change that breaks the original.
//
// What the expectation MEANS here: the VALUE each of the four fields should
// carry after the reading has grounded and coerced it — the string that would
// reach the deal, not the words the model chose around it. That is the level a
// document can be right or wrong at: an order form states one close date, and
// a reply that read it as a different one is wrong however well it wrote.
//
// A field the scenario does not name must come back OMITTED. That is what
// makes the empty expectation meaningful, and the empty expectation is the
// important one: plenty of real documents state none of the four, and a site
// that invents an amount for one of them writes a number onto a deal that
// nobody in the room ever agreed.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/ports/extraction"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// documentFieldsFixture is ONE document in exactly what the reading hands the
// model: its text, or its bytes with the media type they are.
//
// The two are the site's two lanes (RD-PARAM-N-4) and a scenario picks one by
// filling one. Bytes ride base64 because a corpus scenario is a YAML file: a
// scanned form has no other honest representation there, and a path to a file
// beside it would put the fixture's meaning outside the fixture.
type documentFieldsFixture struct {
	Text     string `json:"text"`
	MIME     string `json:"mime"`
	Bytes    []byte `json:"bytes"`
	Filename string `json:"filename"`
}

// documentFieldsCases serves the one site that reads deal facts out of an
// attached document.
type documentFieldsCases struct{}

func (documentFieldsCases) Site() aitasks.Site {
	return aitasks.Site{
		Task:    ai.TaskDocumentExtract,
		Variant: laneFields,
		Kind:    ai.SiteKindOneShot,
	}
}

// CertifiedScope is the site's own kind: reading a document IS one call. There
// is no solo re-ask here — a field under the floor is omitted, not asked about
// again — so the call this case makes is the whole path.
func (documentFieldsCases) CertifiedScope() string { return aitasks.ScopeFullInvocation }

// Prepare turns one document and the values the scenario expects from it into a
// runnable case.
//
//nolint:ireturn // PreparedCase IS the seam: one implementation per site behind the one interface the cert lane runs.
func (documentFieldsCases) Prepare(fixture, expected json.RawMessage) (aitasks.PreparedCase, error) {
	var f documentFieldsFixture
	if err := json.Unmarshal(fixture, &f); err != nil {
		return nil, fmt.Errorf("document_extract/fields: the fixture is not the shape this site takes: %w", err)
	}
	src, err := sourceFromFixture(f)
	if err != nil {
		return nil, err
	}
	var want map[string]string
	if err := json.Unmarshal(expected, &want); err != nil {
		return nil, fmt.Errorf(
			"document_extract/fields: the expected answer is not a map of field name to the value it should carry: %w", err)
	}
	if err := refuseUnreachableFields(want); err != nil {
		return nil, err
	}
	return &documentFieldsCase{src: src, expected: want}, nil
}

// sourceFromFixture names a document the reading could never have been given,
// and otherwise builds the source in the shape the engine builds it.
func sourceFromFixture(f documentFieldsFixture) (documentSource, error) {
	text := strings.TrimSpace(f.Text)
	switch {
	case text != "" && len(f.Bytes) > 0:
		return documentSource{}, errors.New(
			"document_extract/fields: the fixture supplies both text and bytes, and a reading takes one lane or the other")
	case text == "" && len(f.Bytes) == 0:
		return documentSource{}, errors.New(
			"document_extract/fields: the fixture supplies no document, so there is nothing to read")
	case text != "":
		if len(text) > maxDocumentTextChars {
			return documentSource{}, fmt.Errorf(
				"document_extract/fields: the fixture is %d characters, and one reading addresses at most %d",
				len(text), maxDocumentTextChars)
		}
		return documentSource{Text: text, Filename: f.Filename}, nil
	}
	if len(f.Bytes) > maxDocumentBytes {
		return documentSource{}, fmt.Errorf(
			"document_extract/fields: the fixture is %d bytes, and one reading carries at most %d",
			len(f.Bytes), maxDocumentBytes)
	}
	if !model.CarriesMIME(carriedDocumentMIMEs(), f.MIME) {
		return documentSource{}, fmt.Errorf(
			"document_extract/fields: the fixture is %q, which no adapter that carries documents accepts — "+
				"the reading would never have reached a model with it", f.MIME)
	}
	return documentSource{
		Part:     model.Attachment{MIME: f.MIME, Bytes: f.Bytes, Name: f.Filename},
		Filename: f.Filename,
	}, nil
}

// carriedDocumentMIMEs is what a bytes-lane fixture may declare: the types some
// shipping adapter carries. Asked of the adapters rather than written down here,
// so a corpus cannot pin a media type no binding could ever have been handed.
func carriedDocumentMIMEs() []string { return ai.DocumentMIMEs() }

// refuseUnreachableFields names an expectation the site can never satisfy,
// which would measure nothing for as long as it stayed in the corpus. An EMPTY
// expectation is not one of them — it is the abstention scenario, and it is the
// one many documents deserve.
//
// The values are checked against the SAME coercion the reading applies, so a
// scenario cannot expect an amount the deal could not hold or a currency the
// money type would refuse. Sorted, so an expectation with two offences names
// the same one every time.
func refuseUnreachableFields(want map[string]string) error {
	for _, name := range slices.Sorted(maps.Keys(want)) {
		if !isDocumentField(name) {
			return fmt.Errorf(
				"document_extract/fields: the scenario expects %q, which is not a field this site reads for", name)
		}
		if strings.TrimSpace(want[name]) == "" {
			return fmt.Errorf(
				"document_extract/fields: the scenario expects %q to be empty; a field the document does not state is expressed by leaving it out",
				name)
		}
		// The amount is scaled against the currency the reading grounds, which a
		// scenario states separately — so it is checked as the integer it will
		// have become rather than through the raw coercion.
		if name == documentFieldAmount {
			continue
		}
		if _, ok := coerceDocumentValue(name, want[name]); !ok {
			return fmt.Errorf(
				"document_extract/fields: the scenario expects %s = %q, which this site could never write onto a deal",
				name, want[name])
		}
	}
	if _, amount := want[documentFieldAmount]; amount {
		if _, currency := want[documentFieldCurrency]; !currency {
			return errors.New(
				"document_extract/fields: the scenario expects an amount and no currency, and an amount is scaled by the currency the same reading found")
		}
	}
	return nil
}

// documentFieldsCase is one document ready to be read, closed over the values
// the scenario expects from it.
type documentFieldsCase struct {
	src      documentSource
	expected map[string]string
}

// Run issues the one request this site sends, bare: production wraps the same
// request in the shape-retry when the brain supports one, and a case that did
// too would certify the answer a model gives after being told to try again.
func (c *documentFieldsCase) Run(ctx context.Context, completer aitasks.Completer) (aitasks.Trace, error) {
	req := documentExtractRequest(c.src)
	trace := aitasks.Trace{Requests: []model.Request{req}}
	resp, err := completer.Complete(ctx, req)
	if err != nil {
		return trace, fmt.Errorf("document_extract/fields: %w", err)
	}
	trace.Output = resp.Text
	return trace, nil
}

// Evaluate applies the engine's own checks in the engine's own order — the
// shape validator against the document that was asked about, then the grounding
// and coercion — and only then asks whether the values are the ones the
// scenario expects. A reply that fails the validator has no fields to disagree
// with.
//
// The confidence floor IS applied here, unlike the transcript site's, and the
// difference is what the floor decides. There, a hedged but correct reading is
// still a correct reading the engine chose not to act on. Here the floor
// decides whether a value is OFFERED at all, so a reading too unsure to offer
// an amount has, as far as any human will ever see, not read the amount.
func (c *documentFieldsCase) Evaluate(trace aitasks.Trace) aitasks.Outcome {
	if err := documentShapeValid(c.src)(trace.Output); err != nil {
		return aitasks.Outcome{Result: aitasks.OutcomeInvalid, Detail: err.Error()}
	}
	fields, err := readDocumentFields(trace.Output)
	if err != nil {
		return aitasks.Outcome{Result: aitasks.OutcomeInvalid, Detail: err.Error()}
	}
	if disagreements := c.disagreements(fields); len(disagreements) > 0 {
		return aitasks.Outcome{Result: aitasks.OutcomeWrongAnswer, Detail: strings.Join(disagreements, "; ")}
	}
	return aitasks.Outcome{Result: aitasks.OutcomeAccepted}
}

// disagreements names every field the scenario expects and the reading did not
// ground, every field it grounded differently, and every field it offered that
// the document does not state — all of them, because a reading that got one of
// three right is not the near miss one line would read as.
//
// The invented field is the one that matters most. A missed value costs a rep
// the typing they would have done anyway; an invented one puts a number on a
// deal that nobody agreed to, and it arrives wearing a quote.
func (c *documentFieldsCase) disagreements(fields []extraction.ExtractedField) []string {
	grounded := make(map[string]string, len(fields))
	for _, f := range fields {
		if !f.Omitted {
			grounded[f.Field] = f.Value
		}
	}
	var out []string
	for _, name := range documentFieldOrder() {
		want, expected := c.expected[name]
		got, offered := grounded[name]
		switch {
		case expected && !offered:
			out = append(out, fmt.Sprintf("the document states %s and the reading did not offer it", name))
		case expected && !valueAgrees(name, got, want):
			out = append(out, fmt.Sprintf("the reading offers %s = %q where the document states %q", name, got, want))
		case !expected && offered:
			out = append(out, fmt.Sprintf(
				"the reading offers %s = %q, which this document does not state", name, got))
		}
	}
	return out
}

// valueAgrees compares one value the way that field can be right or wrong.
//
// Three of the four have exactly one correct answer — an amount in minor units,
// an ISO-4217 code, a calendar date — and are compared exactly. The deal NAME is
// prose the model chooses: an order form headed "Order Form — Pallet Handling
// Programme, Graz site" can honestly be called either the whole heading or the
// programme it names, and a scenario that demanded one spelling would grade
// PHRASING rather than reading. Its wording is the rubric's to judge; what is
// asserted here is that the reading found the right thing to name at all, which
// containment (either way round, case-folded) is exactly strong enough to say.
//
// This is the transcript site's rule applied to the one free-text field here:
// that case asserts the LINE a commitment was read from and leaves the summary
// and owner to the rubric, for the same reason.
func valueAgrees(field, got, want string) bool {
	if field != documentFieldName {
		return got == want
	}
	a := strings.ToLower(strings.TrimSpace(got))
	b := strings.ToLower(strings.TrimSpace(want))
	if a == "" || b == "" {
		return a == b
	}
	return strings.Contains(a, b) || strings.Contains(b, a)
}
