// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// What a reply must satisfy before a single value out of it is grounded.
//
// Split from documentextract.go because it is one concept and a long one: the
// closed field set, the evidence every stated field owes, and — the part that
// took two revisions to get right — whether a quote actually supports the value
// standing next to it.

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
)

// documentShapeValid is the §5.2 validator: the closed field set respected, and
// every stated field carrying evidence this site may act on.
//
// On the TEXT lane it also holds each quote to the document's own words. That
// check is the whole point there — a value whose quote is not in the document
// was not read out of it — and it is exactly what the bytes lane cannot do,
// since an image has no text to search. Nothing here pretends otherwise: the
// bytes lane's grounding is held by the prompt and by the certification rubric,
// and RD-AC-N-4 says so in as many words.
func documentShapeValid(src documentSource) ai.Validator {
	return func(text string) error {
		var payload documentPayload
		if err := json.Unmarshal([]byte(ai.Unfence(text)), &payload); err != nil {
			return fmt.Errorf("output is not the required JSON shape: %w", err)
		}
		if msg := validateDocumentPayload(payload, src); msg != "" {
			return errors.New(msg)
		}
		return nil
	}
}

// validateDocumentPayload names the first fidelity violation, or "" when the
// payload is one this site may act on.
func validateDocumentPayload(payload documentPayload, src documentSource) string {
	if payload.Fields == nil {
		return "the reply carries no fields key, so it did not answer the question"
	}
	seen := make(map[string]bool, len(documentFieldOrder()))
	for _, field := range payload.fields() {
		if !isDocumentField(field.Field) {
			return fmt.Sprintf("the reply reports %q, which is not one of the fields this document was read for",
				clampToken(field.Field))
		}
		if seen[field.Field] {
			return fmt.Sprintf("the reply reports %q twice, and a document states a value once", field.Field)
		}
		seen[field.Field] = true
		if msg := validateDocumentField(field, src); msg != "" {
			return msg
		}
	}
	// Every field asked for must come back with an answer, even "not stated".
	// A reply that simply leaves one out is an INCOMPLETE reading, and grounding
	// it anyway would report the missing field as a document that is silent
	// about it — a claim about the document made from a fact about the reply.
	for _, name := range documentFieldOrder() {
		if !seen[name] {
			return fmt.Sprintf("the reply says nothing about %q, so the reading is incomplete rather than the document silent", name)
		}
	}
	return ""
}

// isDocumentField reports whether a reported name is one this site asked for. A
// reply that answers a question nobody asked is refused whole rather than
// filtered: the extra field is evidence the reading did not read the prompt,
// which makes the fields it DID report worth less trust, not more.
func isDocumentField(name string) bool {
	for _, known := range documentFieldOrder() {
		if name == known {
			return true
		}
	}
	return false
}

// validateDocumentField holds one reported field to what a document can
// support. Every echoed token is MODEL output — someone who got the model to
// obey can choose it — so anything reaching a log or a retry prompt is bounded.
func validateDocumentField(field documentField, src documentSource) string {
	switch field.Stated {
	case documentStated, documentNotStated:
	default:
		return fmt.Sprintf("field %q does not say whether the document states it, which is the question", field.Field)
	}
	if field.Stated == documentNotStated {
		// An unstated field carries no evidence to check, and demanding empty
		// values would fail a reply for being tidy in a way nobody asked for.
		return ""
	}
	switch {
	case strings.TrimSpace(field.Value) == "":
		return fmt.Sprintf("field %q is reported as stated but carries no value", field.Field)
	case len(field.Value) > maxDocumentValue:
		return fmt.Sprintf("field %q carries a %d-character value, and at most %d may be reported",
			field.Field, len(field.Value), maxDocumentValue)
	case strings.TrimSpace(field.Quote) == "":
		return fmt.Sprintf("field %q cites no quote, and an uncited value is a guess", field.Field)
	case len(field.Quote) > maxDocumentQuote:
		return fmt.Sprintf("field %q cites %d characters, and at most %d may be quoted — a quote locates a value, it does not summarize the page",
			field.Field, len(field.Quote), maxDocumentQuote)
	case strings.TrimSpace(field.Where) == "":
		return fmt.Sprintf("field %q names no page or section, so its quote could not be found again", field.Field)
	case len(field.Where) > maxDocumentWhere:
		return fmt.Sprintf("field %q names a %d-character location, and at most %d may be reported",
			field.Field, len(field.Where), maxDocumentWhere)
	case field.Confidence < 0 || field.Confidence > 1:
		return fmt.Sprintf("field %q reports confidence %v, which is outside [0,1]", field.Field, field.Confidence)
	}
	if src.onTextLane() && !quotedFromDocument(src.Text, field.Quote) {
		return fmt.Sprintf("field %q quotes text this document does not contain, so the value was not read out of it", field.Field)
	}
	if !valueSupportedByQuote(field) {
		return fmt.Sprintf(
			"field %q reports a value its own quote does not contain, so the quote grounds something else", field.Field)
	}
	return ""
}

// valueSupportedByQuote checks that a value is actually IN the text cited for
// it, for the two fields where that question has a yes-or-no answer.
//
// A quote that appears in the document is not yet evidence for the value beside
// it: an invoice stating "Contract value: EUR 148,500.00" and "Deposit: EUR
// 500.00" can yield an amount of 500 cited to the contract line, and every check
// up to here passes — the quote is real, the number is real, and they are about
// different money.
//
// It compares TOKENS, not substrings, and that is the whole difficulty: 500.00
// sits inside 148,500.00, so a containment test would accept exactly the case
// this exists to refuse. The quote's numbers are pulled out whole and the value
// must equal one of them.
//
// The close date is deliberately exempt: it is REFORMATTED to YYYY-MM-DD, so
// "31 January 2027" legitimately grounds "2027-01-31" and demanding agreement
// would refuse every correctly-read date. The name is exempt because it is prose
// the model composes from the document's own words. Neither exemption is worth
// closing with a looser check that admitted the money case too.
func valueSupportedByQuote(field documentField) bool {
	switch field.Field {
	case documentFieldAmount:
		want := stripGrouping(field.Value)
		for _, token := range numberTokens(field.Quote) {
			if token == want {
				return true
			}
		}
		return false
	case documentFieldCurrency:
		want := strings.ToUpper(strings.TrimSpace(field.Value))
		for _, token := range wordTokens(field.Quote) {
			if token == want {
				return true
			}
		}
		return false
	}
	return true
}

// quotedFromDocument reports whether a quote is the document's own words.
//
// Whitespace is collapsed on both sides before comparing, and only whitespace: a
// document's text arrives with the line breaks and column padding its layout
// happened to have, and a reply that reads a value off two lines writes it as
// one sentence. Normalizing more than that — case, punctuation, accents — would
// start admitting quotes the document does not contain, which is the one thing
// this check exists to refuse.
func quotedFromDocument(text, quote string) bool {
	return strings.Contains(collapseSpace(text), collapseSpace(quote))
}

func collapseSpace(s string) string { return strings.Join(strings.Fields(s), " ") }

// numberTokens pulls the whole figures out of a quote — every maximal run of
// digits and the separators a printed amount carries — and normalizes each the
// way this system writes one. Whole runs are what stop 500.00 matching inside
// 148,500.00.
func numberTokens(quote string) []string {
	var out []string
	var current strings.Builder
	flush := func() {
		if current.Len() > 0 {
			out = append(out, stripGrouping(current.String()))
			current.Reset()
		}
	}
	for _, r := range quote {
		if (r >= '0' && r <= '9') || r == ',' || r == '.' || r == '\'' {
			current.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	return out
}

// stripGrouping drops the thousands separators a document prints and this
// system's own value does not, keeping the decimal point that carries meaning.
// A trailing separator is dropped with them, so "148,500.00." from the end of a
// sentence still reads as the figure it is.
func stripGrouping(s string) string {
	s = strings.NewReplacer(",", "", " ", "", "'", "").Replace(strings.TrimSpace(s))
	return strings.TrimSuffix(s, ".")
}

// wordTokens splits a quote into its alphanumeric words, upper-cased, so a
// currency code is matched as a WORD — "EUR" in "EUR 148,500.00" agrees, and
// the "EUR" inside a longer token does not.
func wordTokens(quote string) []string {
	return strings.FieldsFunc(strings.ToUpper(quote), func(r rune) bool {
		return (r < 'A' || r > 'Z') && (r < '0' || r > '9')
	})
}
