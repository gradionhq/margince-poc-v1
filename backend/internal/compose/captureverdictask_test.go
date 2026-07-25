// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The verdict payload validator is a security control, not a parsing
// convenience: it is what stops a model — or a sender who talked one into
// obliging — from answering about an address nobody asked about, answering
// twice, or inventing a verdict outside the closed set. Each rejection below is
// a distinct way the batch contract can be broken.

import (
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestValidateVerdictPayloadRejectsEveryBrokenBatchContract(t *testing.T) {
	asked := capture.PendingCounterparty{ID: ids.NewV7()}
	other := ids.NewV7()
	batch := []capture.PendingCounterparty{asked}
	ok := verdictResult{ID: asked.ID.String(), Verdict: capture.PendingStatusReal, Confidence: 0.9}

	cases := []struct {
		name    string
		results []verdictResult
		wantMsg string
	}{
		{
			name:    "an exact answer is accepted",
			results: []verdictResult{ok},
		},
		{
			// The batch-injection concern in miniature: an answer about someone
			// who was not in this call must never be applied.
			name:    "an id nobody asked about",
			results: []verdictResult{{ID: other.String(), Verdict: capture.PendingStatusReal, Confidence: 0.9}},
			wantMsg: "was not requested",
		},
		{
			name:    "the same id answered twice",
			results: []verdictResult{ok, ok},
			wantMsg: "appears twice",
		},
		{
			// `unsure` is deliberately not in the vocabulary — abstention is
			// derived from the floor, never self-declared.
			name:    "a verdict outside the closed set",
			results: []verdictResult{{ID: asked.ID.String(), Verdict: capture.PendingStatusUnsure, Confidence: 0.9}},
			wantMsg: "is not real|noise",
		},
		{
			name:    "confidence above one",
			results: []verdictResult{{ID: asked.ID.String(), Verdict: capture.PendingStatusReal, Confidence: 1.5}},
			wantMsg: "outside [0,1]",
		},
		{
			name:    "confidence below zero",
			results: []verdictResult{{ID: asked.ID.String(), Verdict: capture.PendingStatusReal, Confidence: -0.1}},
			wantMsg: "outside [0,1]",
		},
		{
			// A silently dropped id would leave its row claimed but unjudged.
			name:    "a requested id left out",
			results: []verdictResult{},
			wantMsg: "is missing from the results",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := validateVerdictPayload(verdictPayload{Results: tc.results}, batch)
			if tc.wantMsg == "" {
				if got != "" {
					t.Fatalf("a valid payload was rejected: %s", got)
				}
				return
			}
			if !strings.Contains(got, tc.wantMsg) {
				t.Fatalf("got %q, want a message containing %q", got, tc.wantMsg)
			}
		})
	}
}

// Whatever the validator echoes back is MODEL output, which a sender who got the
// model to obey can choose. It reaches the operator's log, so it must be bounded
// — otherwise the log is a writing surface.
func TestValidationMessagesDoNotEchoUnboundedModelText(t *testing.T) {
	batch := []capture.PendingCounterparty{{ID: ids.NewV7()}}
	flood := strings.Repeat("A", 100_000)
	msg := validateVerdictPayload(verdictPayload{
		Results: []verdictResult{{ID: flood, Verdict: capture.PendingStatusReal, Confidence: 0.9}},
	}, batch)

	if msg == "" {
		t.Fatal("an unrequested id was accepted")
	}
	if len(msg) > 500 {
		t.Fatalf("the validation message is %d bytes — model-chosen text must be clamped before it reaches a log", len(msg))
	}
}

// The prompt truncates each body on a RUNE boundary, so a multi-byte script is
// cut where the count says and the prompt stays valid text rather than ending
// in half a character.
func TestTruncateRunesCutsOnACharacterBoundary(t *testing.T) {
	// Four-byte runes: a byte-wise cut would split one and produce U+FFFD.
	body := strings.Repeat("😀", 10)
	got := truncateRunes(body, 4)
	if want := strings.Repeat("😀", 4); got != want {
		t.Fatalf("truncateRunes = %q, want %q", got, want)
	}
	if strings.ContainsRune(got, '�') {
		t.Fatal("the cut landed mid-rune")
	}
	if short := truncateRunes("abc", 10); short != "abc" {
		t.Fatalf("a string under the limit was altered: %q", short)
	}
}
