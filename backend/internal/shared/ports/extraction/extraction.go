// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package extraction is the Tier-0 seam for staged, evidence-grounded
// document field extraction (RD-T10). It mirrors ports/retrieval's
// no-op/fixture pattern: the production default is honestly empty until a
// real extractor exists — the future provider rides modules/ai's
// CompleteStructured, injected by compose the same way fieldcatalog's
// Reader is (a module never imports a sibling).
package extraction

import "context"

// ExtractedField is one attempted grounded field, or one omitted field when
// Omitted is true. A non-omitted field always carries the evidence that
// grounds it (SourceQuote/PageOrSection/Confidence) — GATE-AI-1's
// evidence-or-omit invariant: never a guessed value.
type ExtractedField struct {
	Field         string
	Value         string
	SourceQuote   string
	PageOrSection string
	Confidence    string
	Omitted       bool
	OmittedReason string
}

// Extractor is the staged AI-extraction seam: one attachment in, its
// grounded-or-omitted fields out. Implementations must never guess a value
// they cannot ground in the source text.
type Extractor interface {
	Extract(ctx context.Context, attachmentID string) ([]ExtractedField, error)
}

// NoOpExtractor is the production default. It answers honestly empty
// because no document-extraction/OCR/LLM pipeline exists yet — not a 501,
// since the read is validly empty per the contract.
type NoOpExtractor struct{}

// Extract returns no fields and no error because no production extractor exists.
func (NoOpExtractor) Extract(context.Context, string) ([]ExtractedField, error) {
	return nil, nil
}

// FixtureExtractor returns pre-seeded extraction rows keyed by attachment ID,
// for tests and demo harnesses.
type FixtureExtractor struct {
	Fields map[string][]ExtractedField
}

// Extract returns the seeded rows for the attachment ID, or an empty result
// for an unknown attachment.
func (f FixtureExtractor) Extract(_ context.Context, attachmentID string) ([]ExtractedField, error) {
	if f.Fields == nil {
		return nil, nil
	}
	return f.Fields[attachmentID], nil
}
