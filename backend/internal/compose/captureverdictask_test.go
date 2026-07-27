// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The verdict payload validator is a security control, not a parsing
// convenience: it is what stops a model — or a sender who talked one into
// obliging — from answering about an address nobody asked about, answering
// twice, or inventing a verdict outside the closed set. Each rejection below is
// a distinct way the one-sender contract can be broken.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestValidateVerdictPayloadRejectsEveryBrokenBatchContract(t *testing.T) {
	asked := capture.PendingCounterparty{ID: ids.NewV7()}
	other := ids.NewV7()
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
			// An answer about someone who was not the subject of this call must
			// never be applied.
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
			got := validateVerdictPayload(verdictPayload{Results: tc.results}, asked)
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
	asked := capture.PendingCounterparty{ID: ids.NewV7()}
	flood := strings.Repeat("A", 100_000)
	msg := validateVerdictPayload(verdictPayload{
		Results: []verdictResult{{ID: flood, Verdict: capture.PendingStatusReal, Confidence: 0.9}},
	}, asked)

	if msg == "" {
		t.Fatal("an unrequested id was accepted")
	}
	if len(msg) > 500 {
		t.Fatalf("the validation message is %d bytes — model-chosen text must be clamped before it reaches a log", len(msg))
	}
}

// The bound model intermittently returns a confidence declared type: number as
// a JSON string. A verdict is a single-answer decision — there is no partial
// result to keep — so a reply that is right but quoted must be read, not
// deferred.
func TestVerdictPayloadReadsAQuotedConfidence(t *testing.T) {
	const reply = `{"results":[{"id":"0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a61","verdict":"noise","confidence":"0.94"}]}`

	var payload verdictPayload
	if err := json.Unmarshal([]byte(reply), &payload); err != nil {
		t.Fatalf("a quoted confidence failed to decode: %v", err)
	}
	if len(payload.Results) != 1 {
		t.Fatalf("got %d results, want 1", len(payload.Results))
	}
	if got := float64(payload.Results[0].Confidence); got != 0.94 {
		t.Errorf("Confidence = %v, want 0.94", got)
	}
}

// Tolerating the wrapper must not tolerate the value. The range gate lives in
// the validator, where the refusal can name what was wrong, so a quoted 1.4 is
// read and then rejected rather than being read as a number the floor accepts.
func TestVerdictPayloadStillRefusesAnOutOfRangeConfidence(t *testing.T) {
	asked := capture.PendingCounterparty{ID: ids.NewV7()}
	reply := `{"results":[{"id":"` + asked.ID.String() + `","verdict":"noise","confidence":"1.4"}]}`

	var payload verdictPayload
	if err := json.Unmarshal([]byte(reply), &payload); err != nil {
		t.Fatalf("a quoted confidence failed to decode: %v", err)
	}
	msg := validateVerdictPayload(payload, asked)
	if msg == "" {
		t.Fatal("a confidence of 1.4 was accepted; want a refusal")
	}
	if !strings.Contains(msg, "outside [0,1]") {
		t.Errorf("refusal %q does not say what was wrong with the value", msg)
	}
}
