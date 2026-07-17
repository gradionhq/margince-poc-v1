// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

// openAICompatClient is the shared OpenAI-wire transport (/v1/chat/completions,
// /v1/embeddings, response_format json_schema, SSE). Reused by the local vLLM
// binding (apiKey empty, localOnly true) and the cloud openai_compatible binding
// (Bearer key, localOnly false). The trust posture is the caller's choice of
// provider name, never a field on this struct (spec §3.2/§3.6).

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

type openAICompatClient struct {
	http         *http.Client
	baseURL      string
	apiKey       string // "" ⇒ send no Authorization header (local vLLM)
	localOnly    bool   // Caps().LocalOnly — the sovereign-eligibility bit
	defaultModel string
}

// openAICompatSchemaName labels the structured-output schema; OpenAI's
// response_format requires a name, and the value is otherwise opaque.
const openAICompatSchemaName = "structured_output"

type openAICompatChatWire struct {
	Model     string           `json:"model"`
	Messages  []wireMessage    `json:"messages"`
	Tools     []ollamaToolWire `json:"tools,omitempty"`
	MaxTokens int              `json:"max_tokens,omitempty"`
	Stream    bool             `json:"stream"`
	// ResponseFormat carries the OpenAI-compatible json_schema structured
	// output (vLLM guided decoding); set only when the request asks for a
	// schema, so ordinary free-text calls are unchanged.
	ResponseFormat *openAICompatResponseFormat `json:"response_format,omitempty"`
}

// openAICompatResponseFormat / openAICompatJSONSchema mirror the OpenAI
// response_format json_schema shape the endpoint accepts to constrain decoding
// to a schema.
type openAICompatResponseFormat struct {
	Type       string                 `json:"type"` // "json_schema"
	JSONSchema openAICompatJSONSchema `json:"json_schema"`
}

type openAICompatJSONSchema struct {
	Name   string          `json:"name"`
	Schema json.RawMessage `json:"schema"`
	Strict bool            `json:"strict"`
}

type openAICompatChatResponse struct {
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

func (c *openAICompatClient) Complete(ctx context.Context, req model.Request) (model.Response, error) {
	body, err := c.sendChat(ctx, req, false)
	if err != nil {
		return model.Response{}, err
	}
	//craft:ignore swallowed-errors best-effort close of a response body already read to completion — the decode result decides the outcome
	defer func() { _ = body.Close() }()
	var out openAICompatChatResponse
	if err := json.NewDecoder(body).Decode(&out); err != nil {
		return model.Response{}, fmt.Errorf("ai: openai-compat: decode response: %w", err)
	}
	if len(out.Choices) == 0 {
		return model.Response{}, fmt.Errorf("ai: openai-compat: response has no choices")
	}
	return model.Response{
		Text:         out.Choices[0].Message.Content,
		InputTokens:  out.Usage.PromptTokens,
		OutputTokens: out.Usage.CompletionTokens,
	}, nil
}

//nolint:ireturn // model.Client.Stream returns the port's TokenStream interface by contract
func (c *openAICompatClient) Stream(ctx context.Context, req model.Request) (model.TokenStream, error) {
	body, err := c.sendChat(ctx, req, true)
	if err != nil {
		return nil, err
	}
	return &openAICompatStream{body: body, scanner: bufio.NewScanner(body)}, nil
}

func (c *openAICompatClient) Embed(ctx context.Context, req model.EmbedRequest) (model.Embeddings, error) {
	return openAIWireEmbed(ctx, c.post, c.defaultModel, req)
}

func (c *openAICompatClient) Caps() model.Capabilities {
	// EmbedDims stays 0 (unknown): the width is a property of whichever
	// model the deployment serves, discovered from the first Embed call.
	// LocalOnly is the provider's trust posture (vllm true, openai_compatible
	// false), fixed at construction — never a wire-visible property.
	return model.Capabilities{Streaming: true, EmbedDims: 0, LocalOnly: c.localOnly}
}

// attachmentUnsupported returns ErrAttachmentUnsupported (provider-tagged) if
// any attachment's MIME fails allow; nil otherwise. The map-or-reject invariant
// (spec §3.8): no adapter may silently drop an attachment.
func attachmentUnsupported(provider string, atts []model.Attachment, allow func(mime string) bool) error {
	for _, a := range atts {
		if !allow(a.MIME) {
			return fmt.Errorf("ai: %s: %s: %w", provider, a.MIME, model.ErrAttachmentUnsupported)
		}
		// Bytes XOR URI (model.Attachment): both-set would silently drop the
		// inline bytes, neither-set would emit an empty content part — reject both.
		if (len(a.Bytes) == 0) == (a.URI == "") {
			return fmt.Errorf("ai: %s: attachment %q needs exactly one of inline bytes or a uri", provider, a.MIME)
		}
	}
	return nil
}

func isImage(mime string) bool { return strings.HasPrefix(mime, "image/") }

// rejectAllAttachments is the allow-predicate for an adapter whose wire carries
// no attachment parts: nothing passes, so every attachment is rejected with the
// sentinel (honest map-or-reject, never a silent drop).
func rejectAllAttachments(string) bool { return false }

func (c *openAICompatClient) sendChat(ctx context.Context, req model.Request, stream bool) (io.ReadCloser, error) {
	// This text-only chat wire carries no attachment parts, so reject every
	// attachment rather than accept-then-drop it (spec §3.8 map-or-reject). The
	// generic endpoint's multimodal support is model-dependent; mapping images to
	// image_url content parts on this shared wire is a follow-up, and until it
	// lands an honest rejection beats a silent drop.
	if err := attachmentUnsupported("openai-compat", req.Attachments, rejectAllAttachments); err != nil {
		return nil, err
	}
	wire := openAICompatChatWire{Model: req.Model, Stream: stream, MaxTokens: req.MaxTokens}
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
		wire.ResponseFormat = &openAICompatResponseFormat{
			Type:       jsonSchemaFormatType,
			JSONSchema: openAICompatJSONSchema{Name: openAICompatSchemaName, Schema: req.ResponseSchema, Strict: false},
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

func (c *openAICompatClient) post(ctx context.Context, path string, payload []byte) (io.ReadCloser, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("ai: openai-compat: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ai: openai-compat: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		//craft:ignore swallowed-errors best-effort close on the error path — the API status error is the answer
		defer func() { _ = resp.Body.Close() }()
		return nil, openAICompatError(resp)
	}
	return resp.Body, nil
}

// openAICompatError surfaces the vendor's structured error type/message only —
// never the raw response body, which may be unstructured HTML/text — so a logged
// failure can't echo the request or leak provider internals (the anthropic /
// openai pattern). The read error on this already-failed path is not actionable.
func openAICompatError(resp *http.Response) error {
	var apiErr struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if json.Unmarshal(raw, &apiErr) == nil && apiErr.Error.Message != "" {
		return fmt.Errorf("ai: openai-compat: %s: %s (http %d)", apiErr.Error.Type, apiErr.Error.Message, resp.StatusCode)
	}
	return fmt.Errorf("ai: openai-compat: http %d", resp.StatusCode)
}

// openAICompatStream reads the OpenAI-compatible SSE stream: `data: {...}`
// lines, terminated by `data: [DONE]`.
type openAICompatStream struct {
	body    io.ReadCloser
	scanner *bufio.Scanner
}

type openAICompatStreamEvent struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
}

func (s *openAICompatStream) Next(ctx context.Context) (string, bool, error) {
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
		var ev openAICompatStreamEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return "", false, fmt.Errorf("ai: openai-compat: stream event: %w", err)
		}
		if len(ev.Choices) > 0 && ev.Choices[0].Delta.Content != "" {
			return ev.Choices[0].Delta.Content, true, nil
		}
	}
	if err := s.scanner.Err(); err != nil {
		return "", false, fmt.Errorf("ai: openai-compat: stream: %w", err)
	}
	return "", false, nil
}

func (s *openAICompatStream) Close() error { return s.body.Close() }
