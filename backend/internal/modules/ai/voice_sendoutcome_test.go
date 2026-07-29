// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"math"
	"testing"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// similarityTolerance keeps the assertions on the pinned ratio honest
// without asserting bit-exact float arithmetic: 1 - 1/5 is not
// representable, so an exact compare would test IEEE-754, not the metric.
const similarityTolerance = 1e-9

// The metric is pinned, not incidental: a later PR promotes these signals
// into a training corpus retroactively, so every stored similarity must
// mean the same thing forever. It is a normalized token-level Levenshtein
// ratio over NFC-normalized, case-folded, whitespace-collapsed text.
func TestClassifyVoiceSendOutcomeScoresThePinnedSimilarityMetric(t *testing.T) {
	cases := []struct {
		name           string
		original       string
		final          string
		wantOutcome    string
		wantSimilarity float64
	}{
		{
			name:     "an untouched draft is accepted at full similarity",
			original: "Thanks for the call today", final: "Thanks for the call today",
			wantOutcome: voiceOutcomeAccepted, wantSimilarity: 1,
		},
		{
			name:     "one substituted token costs one edit out of five",
			original: "thanks for the call today", final: "thanks for the meeting today",
			wantOutcome: voiceOutcomeEditedSent, wantSimilarity: 0.8,
		},
		{
			name:     "one inserted token costs one edit out of the longer side",
			original: "thanks for the call", final: "thanks again for the call",
			wantOutcome: voiceOutcomeEditedSent, wantSimilarity: 0.8,
		},
		{
			name:     "whitespace-only differences normalize to the same text",
			original: "Thanks\n for  the call", final: "Thanks for the call",
			wantOutcome: voiceOutcomeAccepted, wantSimilarity: 1,
		},
		{
			name:     "case-only differences normalize to the same text",
			original: "THANKS for the Call", final: "thanks for the call",
			wantOutcome: voiceOutcomeAccepted, wantSimilarity: 1,
		},
		{
			name: "a combining accent is the same text as its precomposed form",
			// Precomposed é/ô against e/o plus combining marks: the same
			// characters, and a mail client may hand back either form.
			original: "Servus J\u00e9r\u00f4me", final: "Servus Je\u0301ro\u0302me",
			wantOutcome: voiceOutcomeAccepted, wantSimilarity: 1,
		},
		{
			name:     "disjoint text shares nothing",
			original: "alpha beta gamma", final: "delta epsilon",
			wantOutcome: voiceOutcomeEditedSent, wantSimilarity: 0,
		},
		{
			name:     "two empty token lists are equal, not undefined",
			original: "   ", final: "",
			wantOutcome: voiceOutcomeAccepted, wantSimilarity: 1,
		},
		{
			name:     "sending nothing of the draft scores zero",
			original: "alpha beta", final: "",
			wantOutcome: voiceOutcomeEditedSent, wantSimilarity: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outcome, similarity := classifyVoiceSendOutcome(tc.original, tc.final)
			if outcome != tc.wantOutcome {
				t.Errorf("outcome = %q, want %q", outcome, tc.wantOutcome)
			}
			if math.Abs(similarity-tc.wantSimilarity) > similarityTolerance {
				t.Errorf("similarity = %v, want %v", similarity, tc.wantSimilarity)
			}
			if similarity < 0 || similarity > 1 {
				t.Errorf("similarity = %v is outside [0,1], which the column CHECK rejects", similarity)
			}
		})
	}
}

// The metric must be symmetric: which side is the draft and which the sent
// text cannot change the number a future corpus decision reads.
func TestClassifyVoiceSendOutcomeIsSymmetric(t *testing.T) {
	forward, forwardSimilarity := classifyVoiceSendOutcome("thanks for the call today", "thanks for the meeting")
	reverse, reverseSimilarity := classifyVoiceSendOutcome("thanks for the meeting", "thanks for the call today")
	if forward != reverse {
		t.Errorf("outcome = %q forward, %q reversed", forward, reverse)
	}
	if math.Abs(forwardSimilarity-reverseSimilarity) > similarityTolerance {
		t.Errorf("similarity = %v forward, %v reversed", forwardSimilarity, reverseSimilarity)
	}
}

// The DDL vocabulary ('drafted','accepted','edited_sent','rejected') and
// the published event contract (drafted | sent_unedited | sent_edited |
// rejected) disagree on the two sent outcomes. Every emitter goes through
// the payload builder, so the translation is proven there — a caller
// emitting the raw DDL spelling would publish a value no subscriber knows.
func TestVoiceDraftOutcomeRecordedPayloadPublishesTheWireVocabulary(t *testing.T) {
	profileID := ids.NewV7()
	cases := []struct {
		stored string
		want   string
	}{
		{stored: voiceOutcomeDrafted, want: "drafted"},
		{stored: voiceOutcomeAccepted, want: "sent_unedited"},
		{stored: voiceOutcomeEditedSent, want: "sent_edited"},
		{stored: voiceOutcomeRejected, want: "rejected"},
	}
	for _, tc := range cases {
		t.Run(tc.stored, func(t *testing.T) {
			payload := voiceDraftOutcomeRecordedPayload(profileID, tc.stored)
			if payload.Outcome != tc.want {
				t.Errorf("outcome = %q, want %q (the published contract's spelling)", payload.Outcome, tc.want)
			}
			if payload.ProfileId != openapi_types.UUID(profileID) {
				t.Errorf("profile_id = %v, want %v", payload.ProfileId, profileID)
			}
			// Corpus promotion and transformation extraction are a later PR:
			// no emitter has a non-constant value to pass yet.
			if payload.QualifiesAsSource {
				t.Error("qualifies_as_source = true, want false — nothing promotes a signal to a corpus source yet")
			}
			if payload.TransformationCount != 0 {
				t.Errorf("transformation_count = %d, want 0 — no emitter extracts transformations yet", payload.TransformationCount)
			}
		})
	}
}
