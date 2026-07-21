// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

// The Layer-3 opt-in content capture: what of a completion's request and
// response is retained in ai_call_payload, and the caps that keep one
// row bounded.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// capturedMessage is the ai_call_payload wire shape of one request
// message: model.Message with the lowercase JSON keys every payload
// reader expects (the trace UI and the cert-scenario export).
type capturedMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// buildPayload assembles the Layer-3 capture: the request's semantic
// content (system + messages) run through the SAME secret-stripper that
// guards egress, and the model's response text — both as JSON. The
// stripper is the last line before content lands in ai_call_payload, so a
// leaked credential is scrubbed here exactly as it is on the wire.
func (r *Router) buildPayload(ctx context.Context, req model.Request, resp model.Response) (*Payload, error) {
	// Bound the content BEFORE marshaling — truncating the marshaled jsonb
	// bytes would yield invalid JSON. Two limits compose: a per-field cap
	// (system, each message, response) keeps any one text useful-but-bounded,
	// and a request-side aggregate budget keeps a long agent-loop message
	// list from growing the row without limit even with every message
	// individually under the field cap.
	budget := captureBudget{remaining: maxCapturedRequestRunes}
	system := budget.take(req.System)
	// model.Message carries no JSON tags, so marshaling it directly emits
	// "Role"/"Content" — out of step with the lowercase "system"/"messages"
	// envelope keys and with every reader of ai_call_payload (the trace UI
	// and the cert-scenario export both key on lowercase role/content). Map
	// to a locally-tagged shape so the stored document is uniformly lowercase.
	msgs := make([]capturedMessage, len(req.Messages))
	for i, m := range req.Messages {
		msgs[i] = capturedMessage{Role: m.Role, Content: budget.take(m.Content)}
	}
	reqDoc, err := json.Marshal(struct {
		System   string            `json:"system"`
		Messages []capturedMessage `json:"messages"`
	}{system, msgs})
	if err != nil {
		return nil, fmt.Errorf("ai: marshal capture request: %w", err)
	}
	stripped, _, err := req.SecretStripper.Strip(ctx, reqDoc)
	if err != nil {
		return nil, fmt.Errorf("ai: strip capture request: %w", err)
	}
	respDoc, err := json.Marshal(capturePayloadContent(resp.Text))
	if err != nil {
		return nil, fmt.Errorf("ai: marshal capture response: %w", err)
	}
	// The response is stripped exactly like the request: a model that
	// echoes a credential from its context must not land it verbatim in
	// ai_call_payload.
	strippedResp, _, err := req.SecretStripper.Strip(ctx, respDoc)
	if err != nil {
		return nil, fmt.Errorf("ai: strip capture response: %w", err)
	}
	return &Payload{Request: json.RawMessage(stripped), Response: json.RawMessage(strippedResp)}, nil
}

// maxCapturedPayloadRunes caps each captured content field. 16k runes holds a
// generous prompt or response while keeping any single ai_call_payload row
// bounded; it is a rune count (not bytes) so a multi-byte script never inflates
// past the intent, and the cut lands on a rune boundary so the stored JSON
// stays valid after marshaling.
const maxCapturedPayloadRunes = 16_000

// maxCapturedRequestRunes bounds the request side of one ai_call_payload row
// as a whole (system + every message). With the response held to its own
// per-field cap, the row's captured content can never exceed
// maxCapturedRequestRunes + maxCapturedPayloadRunes.
const maxCapturedRequestRunes = 48_000

// captureBudget doles a shared rune allowance across the request's fields;
// each take is also held to the per-field cap.
type captureBudget struct{ remaining int }

func (b *captureBudget) take(s string) string {
	limit := min(maxCapturedPayloadRunes, b.remaining)
	runes := []rune(s)
	if len(runes) <= limit {
		b.remaining -= len(runes)
		return s
	}
	b.remaining -= limit
	return string(runes[:limit]) + captureTruncationMarker
}

// captureTruncationMarker tells a trace reader the stored text is not the
// full content.
const captureTruncationMarker = "…[truncated]"

// capturePayloadContent truncates one captured field to maxCapturedPayloadRunes,
// appending a visible marker so a reader knows the trace is not the full text.
func capturePayloadContent(s string) string {
	runes := []rune(s)
	if len(runes) <= maxCapturedPayloadRunes {
		return s
	}
	return string(runes[:maxCapturedPayloadRunes]) + captureTruncationMarker
}
