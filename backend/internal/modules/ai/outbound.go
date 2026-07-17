// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// Message-role vocabulary shared across adapters — one spelling each so an
// adapter can't drift "assistant" vs "assistants". roleModel is Gemini's
// spelling for the assistant turn.
const (
	roleSystem    = "system"
	roleUser      = "user"
	roleAssistant = "assistant"
	roleModel     = "model"
)

// embedWire is the OpenAI-style embeddings request body ({model, input}); a
// typed struct rather than a map so every adapter spells the two fields the
// same way once.
type embedWire struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

// openAIWireEmbed runs the shared OpenAI /v1/embeddings round-trip: a
// {model, input} request and a {data:[{embedding}]} response. The native openai
// adapter and the openai_compatible transport speak the identical wire, so the
// request build + decode lives here once (each passes its own authenticated
// POST). Providers with a different embeddings shape (ollama, gemini) do not use it.
func openAIWireEmbed(ctx context.Context, post func(context.Context, string, []byte) (io.ReadCloser, error), defaultModel string, req model.EmbedRequest) (model.Embeddings, error) {
	embedModel := req.Model
	if embedModel == "" {
		embedModel = defaultModel
	}
	payload, _, err := sendablePayload(ctx, embedWire{Model: embedModel, Input: req.Inputs}, nil)
	if err != nil {
		return model.Embeddings{}, err
	}
	body, err := post(ctx, "/v1/embeddings", payload)
	if err != nil {
		return model.Embeddings{}, err
	}
	//craft:ignore swallowed-errors best-effort close of a response body already read to completion — the decode result decides the outcome
	defer func() { _ = body.Close() }()
	var out struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(body).Decode(&out); err != nil {
		return model.Embeddings{}, fmt.Errorf("ai: decode embeddings: %w", err)
	}
	vectors := make([][]float32, 0, len(out.Data))
	for _, d := range out.Data {
		vectors = append(vectors, d.Embedding)
	}
	dims := 0
	if len(vectors) > 0 {
		dims = len(vectors[0])
	}
	return model.Embeddings{Vectors: vectors, Dims: dims}, nil
}

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
		out = append(out, wireMessage{Role: roleSystem, Content: system})
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
func sendablePayload[T any](ctx context.Context, body T, stripper model.SecretStripper) ([]byte, model.StripReport, error) {
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
