// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// vllmClient is the second local/self-host adapter (B-EP06.3): a vLLM
// server on the workspace's own infrastructure, spoken over vLLM's
// OpenAI-compatible surface (/v1/chat/completions, /v1/embeddings).
// LocalOnly=true makes it eligible for the sovereign zero-egress
// profile, exactly like the Ollama adapter.
type vllmClient struct {
	http         *http.Client
	baseURL      string
	defaultModel string
}

// vllmSchemaName labels the structured-output schema; OpenAI's
// response_format requires a name, and the value is otherwise opaque.
const vllmSchemaName = "structured_output"

type vllmChatWire struct {
	Model     string           `json:"model"`
	Messages  []wireMessage    `json:"messages"`
	Tools     []ollamaToolWire `json:"tools,omitempty"`
	MaxTokens int              `json:"max_tokens,omitempty"`
	Stream    bool             `json:"stream"`
	// ResponseFormat carries the OpenAI-compatible json_schema structured
	// output (vLLM guided decoding); set only when the request asks for a
	// schema, so ordinary free-text calls are unchanged.
	ResponseFormat *vllmResponseFormat `json:"response_format,omitempty"`
}

// vllmResponseFormat / vllmJSONSchema mirror the OpenAI response_format
// json_schema shape vLLM accepts to constrain decoding to a schema.
type vllmResponseFormat struct {
	Type       string         `json:"type"` // "json_schema"
	JSONSchema vllmJSONSchema `json:"json_schema"`
}

type vllmJSONSchema struct {
	Name   string          `json:"name"`
	Schema json.RawMessage `json:"schema"`
	Strict bool            `json:"strict"`
}

type vllmChatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

func (c *vllmClient) Complete(ctx context.Context, req model.Request) (model.Response, error) {
	body, err := c.sendChat(ctx, req, false)
	if err != nil {
		return model.Response{}, err
	}
	//craft:ignore swallowed-errors best-effort close of a response body already read to completion — the decode result decides the outcome
	defer func() { _ = body.Close() }()
	var out vllmChatResponse
	if err := json.NewDecoder(body).Decode(&out); err != nil {
		return model.Response{}, fmt.Errorf("ai: vllm: decode response: %w", err)
	}
	if len(out.Choices) == 0 {
		return model.Response{}, fmt.Errorf("ai: vllm: response has no choices")
	}
	return model.Response{
		Text:         out.Choices[0].Message.Content,
		InputTokens:  out.Usage.PromptTokens,
		OutputTokens: out.Usage.CompletionTokens,
	}, nil
}

func (c *vllmClient) Stream(ctx context.Context, req model.Request) (model.TokenStream, error) {
	body, err := c.sendChat(ctx, req, true)
	if err != nil {
		return nil, err
	}
	return &vllmStream{body: body, scanner: bufio.NewScanner(body)}, nil
}

func (c *vllmClient) Embed(ctx context.Context, req model.EmbedRequest) (model.Embeddings, error) {
	embedModel := req.Model
	if embedModel == "" {
		embedModel = c.defaultModel
	}
	payload, _, err := sendablePayload(ctx, map[string]any{"model": embedModel, "input": req.Inputs}, nil)
	if err != nil {
		return model.Embeddings{}, err
	}
	body, err := c.post(ctx, "/v1/embeddings", payload)
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
		return model.Embeddings{}, fmt.Errorf("ai: vllm: decode embeddings: %w", err)
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

func (c *vllmClient) Caps() model.Capabilities {
	// EmbedDims stays 0 (unknown): the width is a property of whichever
	// model the deployment serves, discovered from the first Embed call.
	return model.Capabilities{Streaming: true, EmbedDims: 0, LocalOnly: true}
}

func (c *vllmClient) sendChat(ctx context.Context, req model.Request, stream bool) (io.ReadCloser, error) {
	wire := vllmChatWire{Model: req.Model, Stream: stream, MaxTokens: req.MaxTokens}
	if wire.Model == "" {
		wire.Model = c.defaultModel
	}
	// The OpenAI-compatible surface carries the system prompt as the
	// leading message, same as Ollama's chat shape.
	wire.Messages = wireMessages(req.System, req.Messages)
	if len(req.ResponseSchema) > 0 {
		// strict:false: vLLM's guided-decoding backends still constrain to the
		// schema, but this avoids the OpenAI-exact strict rules (every object
		// needs additionalProperties:false + all-required) rejecting a schema
		// the callers don't write that way. The parse→validate→retry policy
		// and the evidence gate remain the real authority regardless.
		wire.ResponseFormat = &vllmResponseFormat{
			Type:       jsonSchemaFormatType,
			JSONSchema: vllmJSONSchema{Name: vllmSchemaName, Schema: req.ResponseSchema, Strict: false},
		}
	}
	for _, tool := range req.Tools {
		var tw ollamaToolWire
		tw.Type = "function"
		tw.Function.Name = tool.Name
		tw.Function.Description = tool.Description
		tw.Function.Parameters = tool.InputSchema
		wire.Tools = append(wire.Tools, tw)
	}
	payload, _, err := sendablePayload(ctx, wire, req.SecretStripper)
	if err != nil {
		return nil, err
	}
	return c.post(ctx, "/v1/chat/completions", payload)
}

func (c *vllmClient) post(ctx context.Context, path string, payload []byte) (io.ReadCloser, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("ai: vllm: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ai: vllm: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		//craft:ignore swallowed-errors best-effort close on the error path — the API status error is the answer
		defer func() { _ = resp.Body.Close() }()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("ai: vllm: http %d: %s", resp.StatusCode, bytes.TrimSpace(raw))
	}
	return resp.Body, nil
}

// vllmStream reads the OpenAI-compatible SSE stream: `data: {...}`
// lines, terminated by `data: [DONE]`.
type vllmStream struct {
	body    io.ReadCloser
	scanner *bufio.Scanner
}

type vllmStreamEvent struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
}

func (s *vllmStream) Next(ctx context.Context) (string, bool, error) {
	for s.scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return "", false, err
		}
		line := strings.TrimSpace(s.scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			return "", false, nil
		}
		var ev vllmStreamEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return "", false, fmt.Errorf("ai: vllm: stream event: %w", err)
		}
		if len(ev.Choices) > 0 && ev.Choices[0].Delta.Content != "" {
			return ev.Choices[0].Delta.Content, true, nil
		}
	}
	if err := s.scanner.Err(); err != nil {
		return "", false, fmt.Errorf("ai: vllm: stream: %w", err)
	}
	return "", false, nil
}

func (s *vllmStream) Close() error { return s.body.Close() }
