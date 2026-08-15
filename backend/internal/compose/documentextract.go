// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Reading one attached document for the deal facts it states (RD-WIRE-N-1),
// and staging each with the quote it was read from.
//
// What this site may claim is bounded by what a document IS: a statement of
// terms. So a field here is always "the document says the amount is X", never
// "this deal is worth X" — the second is a judgment about the account, and no
// invoice states it. Every field cites the text it was read from and writes
// NOTHING to any record until a human accepts it (GATE-AI-1); accepting goes
// through the same RBAC-gated deal update a rep's own edit takes.
//
// It mirrors transcriptpropose.go deliberately — same fenced prompt, same pure
// request builder, same validator-then-floor split — because the two sites do
// the same job on different material, and one shape is what lets a reader who
// knows either one read the other.

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/promptfence"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/values"
	"github.com/gradionhq/margince/backend/internal/shared/ports/extraction"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
	"github.com/gradionhq/margince/backend/internal/shared/schema"
)

const (
	// documentConfidenceFloor: below it the field is OMITTED rather than
	// offered. A value a human has to check against the document themselves has
	// cost them the work the reading was supposed to save, and an amount read
	// unsurely off a scan is exactly the value nobody notices is wrong.
	documentConfidenceFloor = 0.7
	// documentHighConfidence is where the contract's two-band vocabulary splits
	// (RD-PARAM-N-2). Between the floor and here is `medium`: worth showing,
	// worth looking at twice.
	documentHighConfidence = 0.85
	// maxDocumentQuote bounds one field's cited text. A quote is where a value
	// was read, not the paragraph around it — and it is MODEL output derived
	// from a document a counterparty may have written, so it lands in a note a
	// human reads and must not become a wall of text.
	maxDocumentQuote = 300
	// maxDocumentWhere bounds the page/section citation.
	maxDocumentWhere = 80
	// maxDocumentValue bounds a raw value before it is coerced. Every field this
	// site may write is short — a deal name, an amount, a currency code, a date.
	maxDocumentValue = 200
)

// The four fields a reading may propose, which is exactly the closed set the
// accept path can write onto a deal (RD-PARAM-N-3). They are spelled here as
// the model is asked for them, and the coercion below turns each into the
// value setAcceptedDealField takes.
const (
	documentFieldName     = acceptFieldName
	documentFieldAmount   = acceptFieldAmountMinor
	documentFieldCurrency = acceptFieldCurrency
	documentFieldClose    = acceptFieldExpectedClose
)

// omitReasonNotStated and omitReasonNotConfident are the contract's two
// omission reasons. They are different answers and the panel says so: the
// document is silent, versus the document says something the reading could not
// hold steadily enough to offer.
const (
	omitReasonNotStated    = "not_stated_in_file"
	omitReasonNotConfident = "not_confidently_stated"
)

const documentSystem = `You read ONE business document — an order form, an invoice, a quote, a signed
agreement — and report only what it STATES about the deal it records. Report a value only
when the document says it in words or figures you can quote back verbatim. Report nothing
for a value you are inferring, calculating, or carrying over from what documents like this
usually say. A document that does not state a value is normal and common: saying so is the
correct answer, and is worth more than a plausible guess. Quote the exact text each value
was read from, and name the page or section it appears in.

Many attached files record no piece of business at all — a specification, a checklist,
minutes, a set of requirements. Every field is not_stated for such a file. In particular a
document's own TITLE is not the name of a deal: "QA Validation Requirements" and "Packaging
line 3 — revision C" are what a document is called and what it is about, not something
anybody is buying. Report a name only when the document records a purchase, an engagement or
an agreement, and the name is what is being bought or supplied.`

// documentSystemFor names THIS call's data boundary; see promptfence.Fence.Rule.
func documentSystemFor(fence promptfence.Fence) string {
	return documentSystem + "\n" + fence.Rule("document")
}

// documentSource is one document in exactly the form it reaches the model:
// either its own text, or its bytes as a part the binding declared it carries.
// Exactly one of the two, decided by documentLaneFor before a call is built.
type documentSource struct {
	// Text is the document's own text, when it has any without a parser.
	// Non-empty on the text lane, empty on the bytes lane.
	Text string
	// Part is the document as an input part. Zero on the text lane.
	Part model.Attachment
	// Filename is provenance a reader of the prompt can see. It is untrusted
	// like every other byte of the document, and is fenced accordingly.
	Filename string
}

// onTextLane reports which of the two lanes this source takes. It reads off
// Text alone because that is what decides whether a quote can be checked, and a
// second flag saying the same thing is a second thing to keep true.
func (s documentSource) onTextLane() bool { return s.Text != "" }

// documentField is one field as the model reports it.
//
// Stated is an ENUM rather than a boolean, and empty rather than absent is what
// "did not answer" looks like: "this document does not state a close date" is a
// real and common answer that must be distinguishable from a reply that left
// the field out, and a two-word enum says which one it is in the model's own
// output instead of in the shape of the JSON around it.
type documentField struct {
	Field      string            `json:"field"`
	Stated     string            `json:"stated"`
	Value      string            `json:"value"`
	Quote      string            `json:"source_quote"`
	Where      string            `json:"page_or_section"`
	Confidence schema.Confidence `json:"confidence"`
}

// The two answers to "does this document state it".
const (
	documentStated    = "stated"
	documentNotStated = "not_stated"
)

// documentPayload is the reply's shape.
type documentPayload struct {
	Fields *[]documentField `json:"fields"`
}

func (p documentPayload) fields() []documentField {
	if p.Fields == nil {
		return nil
	}
	return *p.Fields
}

// errRefusedDocument is terminal for this reading: the model answered with
// something this site may not act on. It fails the READING, not the job — a
// retry would ask the same question of the same document and get the same
// answer.
var errRefusedDocument = errors.New("compose: the reading could not be used")

// documentExtractRequest builds the model call for one document.
//
// It is a PURE function of the source so the certification case can issue the
// SHIPPING request rather than a copy of it — a cert that grades a
// hand-rewritten prompt certifies nothing about what runs.
func documentExtractRequest(src documentSource) model.Request {
	fence := promptfence.New()
	var prompt strings.Builder
	prompt.WriteString("One business document (untrusted).\n")
	if src.Filename != "" {
		prompt.WriteString(fence.WrapAttr("document", "filename", src.Filename) + "\n")
	}
	if src.onTextLane() {
		prompt.WriteString(fence.WrapAttr("document", "text", src.Text) + "\n")
	} else {
		// FENCED like the filename above it, and for the same reason: the media
		// type is whatever Content-Type the uploader sent, kept on the row as a
		// hint (DOC-PARAM-9) and never validated into a closed set. Written into
		// the prompt bare it would be the one span of counterparty-controlled
		// text speaking in the instruction voice, on the surface whose entire
		// design is that nothing from the document does.
		prompt.WriteString("The document itself is attached to this message.\n")
		prompt.WriteString(fence.WrapAttr("document", "media_type", src.Part.MIME) + "\n")
	}
	fmt.Fprintf(&prompt,
		`Return JSON: { "fields": [ { "field", "stated", "value", "source_quote", "page_or_section", "confidence" } ] } — `+
			`one entry for EACH of %s, in that order, and no others. `+
			`Set "stated" to %q for a field this document states and %q for one it does not, leaving the other values empty when it does not. `+
			`"value" for %s is the amount in the document's own figures with no currency symbol and no thousands separators; `+
			`for %s it is the ISO-4217 code; for %s it is the calendar date as YYYY-MM-DD; `+
			`for %s it is what is being bought or supplied — not the document's own title, and not_stated for a document that records no purchase at all. `+
			`"source_quote" is the exact text the value was read from, copied verbatim — never a paraphrase and never text you composed. `+
			`"page_or_section" names where in the document it appears.`,
		strings.Join(documentFieldOrder(), ", "), documentStated, documentNotStated,
		documentFieldAmount, documentFieldCurrency, documentFieldClose, documentFieldName)

	req := model.Request{
		System:         documentSystemFor(fence),
		Messages:       []model.Message{{Role: chatRoleUser, Content: prompt.String()}},
		MaxTokens:      ai.ReasoningOutputMaxTokens,
		ResponseSchema: documentExtractSchema(),
		SecretStripper: ai.NewSecretStripper(),
	}
	if !src.onTextLane() {
		req.Attachments = []model.Attachment{src.Part}
	}
	return req
}

// documentFieldOrder is the closed field set, in the order the prompt asks for
// them and the order a reading reports them. Derived from nothing — written
// once, here, and read by the prompt, the validator and the coercion alike, so
// widening the set is one edit rather than three that can disagree.
func documentFieldOrder() []string {
	return []string{documentFieldName, documentFieldAmount, documentFieldCurrency, documentFieldClose}
}

// documentExtractSchema is the generation-time shape guardrail.
func documentExtractSchema() json.RawMessage {
	return schema.Must(schema.Object(
		map[string]schema.Node{
			laneFields: schema.Array(schema.Object(
				map[string]schema.Node{
					"field":                 schema.String(),
					"stated":                schema.Enum(documentStated, documentNotStated),
					extractionValueKey:      schema.String(),
					"source_quote":          schema.String(),
					"page_or_section":       schema.String(),
					extractionConfidenceKey: schema.Number(),
				},
				"field", "stated", "value", "source_quote", "page_or_section", extractionConfidenceKey,
			)),
		},
		"fields",
	))
}

// groundDocumentFields turns a validated reply into what the reading stores:
// grounded fields, and honest omissions with the reason each was omitted.
//
// Every field this site asked for comes back, always. A field missing from the
// output entirely would leave the panel unable to say whether the document was
// silent about it or the reading forgot to look — and "the document does not
// state a close date" is information a rep acts on.
func groundDocumentFields(payload documentPayload) []extraction.ExtractedField {
	reported := make(map[string]documentField, len(payload.fields()))
	for _, field := range payload.fields() {
		reported[field.Field] = field
	}
	out := make([]extraction.ExtractedField, 0, len(documentFieldOrder()))
	for _, name := range documentFieldOrder() {
		out = append(out, groundOneField(name, reported[name]))
	}
	return out
}

// groundOneField decides one field's fate: grounded with its evidence, or
// omitted with the reason it was.
func groundOneField(name string, field documentField) extraction.ExtractedField {
	omitted := extraction.ExtractedField{Field: name, Omitted: true, OmittedReason: omitReasonNotStated}
	if field.Stated != documentStated {
		return omitted
	}
	if float64(field.Confidence) < documentConfidenceFloor {
		// Stated, but not steadily enough to offer. A DIFFERENT answer from
		// silence, and the panel renders it differently: a rep who knows the
		// document says something can go and read it.
		omitted.OmittedReason = omitReasonNotConfident
		return omitted
	}
	value, ok := coerceDocumentValue(name, field.Value)
	if !ok {
		// The document states something this field cannot hold — an amount that
		// is not a number, a currency that is not a code. Silence is the wrong
		// word for it, so it takes the same reason a low-confidence read does:
		// the document said something, and the reading could not turn it into a
		// value worth offering.
		omitted.OmittedReason = omitReasonNotConfident
		return omitted
	}
	return extraction.ExtractedField{
		Field:         name,
		Value:         value,
		SourceQuote:   strings.TrimSpace(field.Quote),
		PageOrSection: strings.TrimSpace(field.Where),
		Confidence:    documentConfidenceBand(field.Confidence),
	}
}

// documentConfidenceBand maps the numeric confidence every site asks for onto
// the contract's two-band vocabulary. There is no third band, because a value
// below the floor is not offered at all (RD-PARAM-N-2).
func documentConfidenceBand(confidence schema.Confidence) string {
	if float64(confidence) >= documentHighConfidence {
		return string(crmcontracts.ExtractedFieldConfidenceHigh)
	}
	return string(crmcontracts.ExtractedFieldConfidenceMedium)
}

// coerceDocumentValue turns one reported value into the string the accept path
// takes, refusing anything it could not write.
//
// The arithmetic is HERE and not in the prompt: the model is asked for the
// figure the document prints, and minor units are computed from it by the same
// table that renders them back (values.MinorUnits). A model asked to multiply
// by a hundred is a model that can be wrong by a hundred, and an amount wrong
// by a hundred is exactly the error nobody catches on a scan.
func coerceDocumentValue(name, raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	switch name {
	case documentFieldName:
		return raw, true
	case documentFieldCurrency:
		code := strings.ToUpper(raw)
		if _, err := values.NewMoney(0, code); err != nil {
			return "", false
		}
		return code, true
	case documentFieldClose:
		if _, err := time.Parse(time.DateOnly, raw); err != nil {
			return "", false
		}
		return raw, true
	case documentFieldAmount:
		// The amount is scaled by the CURRENCY's minor-unit count, which this
		// function cannot see — so it is resolved in a second pass over the whole
		// reading, where both fields are in hand (resolveDocumentAmount).
		return raw, true
	}
	return "", false
}

// resolveDocumentAmount scales the amount by the currency the SAME reading
// grounded, and omits it when there is none.
//
// An amount without a currency is not a number this system can store: 12500
// means twelve thousand five hundred euros or a hundred and twenty-five of
// them depending on a value that is not in the field. So the two fields stand
// or fall together, and a document that prints an amount with no recognisable
// currency yields no amount at all rather than one scaled by a guess.
func resolveDocumentAmount(fields []extraction.ExtractedField) []extraction.ExtractedField {
	currency := ""
	for _, f := range fields {
		if f.Field == documentFieldCurrency && !f.Omitted {
			currency = f.Value
		}
	}
	for i, f := range fields {
		if f.Field != documentFieldAmount || f.Omitted {
			continue
		}
		minor, ok := "", false
		if currency != "" {
			var scaled int64
			scaled, ok = values.MinorUnits(f.Value, currency)
			minor = fmt.Sprintf("%d", scaled)
		}
		if !ok {
			fields[i] = extraction.ExtractedField{
				Field: documentFieldAmount, Omitted: true, OmittedReason: omitReasonNotConfident,
			}
			continue
		}
		fields[i].Value = minor
	}
	return fields
}

// readDocumentFields is the whole reading, from a validated reply to what the
// row stores: parse, ground each field, then resolve the amount against the
// currency its own reading found.
//
// The certification case calls THIS, not a copy of it, so what a corpus scores
// is what a deal gets.
func readDocumentFields(output string) ([]extraction.ExtractedField, error) {
	var payload documentPayload
	if err := json.Unmarshal([]byte(ai.Unfence(output)), &payload); err != nil {
		return nil, fmt.Errorf("%w: output is not the required JSON shape: %w", errRefusedDocument, err)
	}
	if payload.Fields == nil {
		return nil, fmt.Errorf("%w: the reply carries no fields key", errRefusedDocument)
	}
	return resolveDocumentAmount(groundDocumentFields(payload)), nil
}
