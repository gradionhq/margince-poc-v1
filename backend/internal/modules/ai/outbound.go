// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// wireMessage is the lowercase JSON shape every provider speaks; the
// port's Message deliberately carries no wire tags (the seam is not a
// serialization contract), so adapters convert here.
type wireMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func wireMessages(system string, msgs []model.Message) []wireMessage {
	out := make([]wireMessage, 0, len(msgs)+1)
	if system != "" {
		out = append(out, wireMessage{Role: "system", Content: system})
	}
	for _, m := range msgs {
		out = append(out, wireMessage{Role: m.Role, Content: m.Content})
	}
	return out
}

// sendablePayload marshals a provider wire body and runs the
// per-request SecretStripper over the marshaled bytes. Every adapter —
// cloud, local, and the fake — puts ONLY the returned bytes on the
// wire, so "secrets never appear in a model-bound payload" holds at the
// last possible moment before egress, not at some earlier layer a code
// path could bypass. If a stripped secret leaves the JSON malformed the
// provider rejects the request; a failed call is the acceptable cost of
// a credential that never left the process.
func sendablePayload(ctx context.Context, body any, stripper model.SecretStripper) ([]byte, model.StripReport, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, model.StripReport{}, fmt.Errorf("ai: marshal request: %w", err)
	}
	if stripper == nil {
		return payload, model.StripReport{}, nil
	}
	stripped, report, err := stripper.Strip(ctx, payload)
	if err != nil {
		return nil, model.StripReport{}, fmt.Errorf("ai: secret stripper: %w", err)
	}
	return stripped, report, nil
}
