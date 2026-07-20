// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestWireAiCallDetailMapsPayloadAndAttempts(t *testing.T) {
	sentinel := "provider_unavailable"
	detail := CallDetail{
		CallSummary: CallSummary{
			ID: ids.NewV7(), OccurredAt: time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC),
			Task: "capture_classify", Tier: "cheap_cloud", Provider: "gemini",
			ModelID: "gemini-2.5-flash", ServedModel: "gemini-2.5-flash",
			Attempt: 2, TokensIn: 100, TokensOut: 20, LatencyMS: 900,
			ErrorSentinel: &sentinel, HasPayload: true,
		},
		ServedIdentitySource: "configured",
		ContextScopes:        []string{"identity"},
		ContextFingerprint:   "abc",
		Attempts: []CallAttempt{
			{Attempt: 1, TokensIn: 100, OccurredAt: time.Date(2026, 7, 20, 9, 59, 0, 0, time.UTC)},
			{
				Attempt: 2, IsTerminal: true, AttemptReason: "retry_on_5xx", TokensIn: 100,
				TokensOut: 20, LatencyMS: 900,
				OccurredAt: time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC),
			},
		},
		Payload: &Payload{
			Request:  json.RawMessage(`{"system":"s","messages":[]}`),
			Response: json.RawMessage(`"ok"`),
		},
	}
	wire := wireAiCall(detail)
	if !wire.PayloadCaptured || wire.Payload == nil {
		t.Fatalf("payload_captured/payload not mapped: %+v", wire)
	}
	if len(wire.Attempts) != 2 || !wire.Attempts[1].IsTerminal {
		t.Fatalf("attempt ladder not mapped: %+v", wire.Attempts)
	}
	if wire.ErrorSentinel == nil || *wire.ErrorSentinel != sentinel {
		t.Fatal("error_sentinel not mapped")
	}
	if wire.CallsAttempted != 2 {
		t.Fatalf("calls_attempted = %d, want 2", wire.CallsAttempted)
	}
}

func TestWireAiCallSummaryCarriesPayloadPresenceOnly(t *testing.T) {
	wire := wireAiCallSummary(CallSummary{ID: ids.NewV7(), Task: "enrich", HasPayload: true})
	if !wire.HasPayload {
		t.Fatal("has_payload lost in mapping")
	}
}
