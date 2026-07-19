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

// anthropicClient is the cloud-frontier adapter (B-EP06.2): the
// Anthropic Messages API over a customer-supplied key (BYOK, ADR-0020 —
// we provide no inference; the key, endpoint and DPA are the
// customer's). stdlib HTTP only: the vendor wire format is small enough
// that an SDK would cost more in dependency surface than it saves.
type anthropicClient struct {
	http         *http.Client
	baseURL      string
	apiKey       string
	defaultModel string
}

const anthropicAPIVersion = "2023-06-01"

// anthropicMaxTokensDefault caps a request that didn't set MaxTokens —
// the API requires the field, and an unbounded default would let a
// caller bug turn into an unbounded spend.
const anthropicMaxTokensDefault = 1024

type anthropicWire struct {
	Model        string                 `json:"model"`
	MaxTokens    int                    `json:"max_tokens"`
	System       string                 `json:"system,omitempty"`
	Messages     []wireMessage          `json:"messages"`
	Tools        []anthropicToolWire    `json:"tools,omitempty"`
	Stream       bool                   `json:"stream,omitempty"`
	OutputConfig *anthropicOutputConfig `json:"output_config,omitempty"`
}

type anthropicToolWire struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// anthropicOutputConfig / anthropicResponseFormat carry Anthropic's native
// structured-output constraint (output_config.format). A json_schema format
// constrains the completion to the schema at generation — the same guardrail
// Ollama's `format` and vLLM's response_format provide — and the completion
// still arrives as an ordinary text block of JSON, so no response handling
// changes. Sent only when the request carries a schema.
type anthropicOutputConfig struct {
	Format *anthropicResponseFormat `json:"format,omitempty"`
}

type anthropicResponseFormat struct {
	Type   string          `json:"type"`
	Schema json.RawMessage `json:"schema"`
}

// streamedCompleteThreshold is the MaxTokens above which Complete rides
// the SSE wire and accumulates: Anthropic (and intermediaries) drop a
// non-streaming connection that stays silent for ~a minute, and a large
// completion produces no bytes until it is done. Small calls keep the
// simple wire.
const streamedCompleteThreshold = 8192

func (c *anthropicClient) Complete(ctx context.Context, req model.Request) (model.Response, error) {
	if req.MaxTokens > streamedCompleteThreshold {
		return c.completeStreamed(ctx, req)
	}
	body, err := c.post(ctx, req)
	if err != nil {
		return model.Response{}, err
	}
	//craft:ignore swallowed-errors best-effort close of a response body already read to completion — the decode result decides the outcome
	defer func() { _ = body.Close() }()
	var out struct {
		Model   string `json:"model"`
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(body).Decode(&out); err != nil {
		return model.Response{}, fmt.Errorf("ai: anthropic: decode response: %w", err)
	}
	// The port's Response carries text only: tasks run in JSON mode
	// (ai-operational-spec §5.1), so a tool_use block is rendered as its
	// JSON rather than dropped silently.
	var text strings.Builder
	for _, block := range out.Content {
		switch block.Type {
		case "text":
			text.WriteString(block.Text)
		case "tool_use":
			blockJSON, _ := json.Marshal(map[string]any{"tool": block.Name, "input": block.Input})
			text.Write(blockJSON)
		}
	}
	return model.Response{
		Text:         text.String(),
		InputTokens:  out.Usage.InputTokens,
		OutputTokens: out.Usage.OutputTokens,
		ServedModel:  out.Model,
	}, nil
}

// completeStreamed is Complete over the SSE wire: text deltas (and
// schema-constrained JSON deltas) accumulate into one response, and the
// usage counts are read off the message_start / message_delta events so
// metering stays exact.
func (c *anthropicClient) completeStreamed(ctx context.Context, req model.Request) (model.Response, error) {
	body, err := c.postStream(ctx, req)
	if err != nil {
		return model.Response{}, err
	}
	//craft:ignore swallowed-errors best-effort close of a stream already consumed to message_stop — the scan result decides the outcome
	defer func() { _ = body.Close() }()

	var text strings.Builder
	var resp model.Response
	scanner := streamLineScanner(body)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return model.Response{}, err
		}
		data, isData := strings.CutPrefix(scanner.Text(), "data: ")
		if !isData {
			continue
		}
		var ev struct {
			Type    string `json:"type"`
			Message struct {
				Model string `json:"model"`
				Usage struct {
					InputTokens int `json:"input_tokens"`
				} `json:"usage"`
			} `json:"message"`
			Delta struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				PartialJSON string `json:"partial_json"`
			} `json:"delta"`
			Usage struct {
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return model.Response{}, fmt.Errorf("ai: anthropic: stream event: %w", err)
		}
		switch ev.Type {
		case "message_start":
			resp.InputTokens = ev.Message.Usage.InputTokens
			resp.ServedModel = ev.Message.Model
		case "content_block_delta":
			text.WriteString(ev.Delta.Text)
			text.WriteString(ev.Delta.PartialJSON)
		case "message_delta":
			resp.OutputTokens = ev.Usage.OutputTokens
		case "message_stop":
			resp.Text = text.String()
			return resp, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return model.Response{}, fmt.Errorf("ai: anthropic: stream: %w", err)
	}
	return model.Response{}, fmt.Errorf("ai: anthropic: stream ended without message_stop")
}

func (c *anthropicClient) Stream(ctx context.Context, req model.Request) (model.TokenStream, error) {
	body, err := c.postStream(ctx, req)
	if err != nil {
		return nil, err
	}
	return &anthropicStream{body: body, scanner: streamLineScanner(body)}, nil
}

// Embed is a different lane, not a chat-tier capability: Anthropic
// serves no embeddings API, and the routing config binds the embed lane
// to a local or fake embedder (ai-operational-spec §1.1).
func (c *anthropicClient) Embed(context.Context, model.EmbedRequest) (model.Embeddings, error) {
	return model.Embeddings{}, fmt.Errorf("ai: anthropic: %w", model.ErrEmbeddingsUnsupported)
}

func (c *anthropicClient) Caps() model.Capabilities {
	return model.Capabilities{Streaming: true, EmbedDims: 0, LocalOnly: false}
}

// post sends one non-streaming Messages call; postStream opens the SSE
// variant of the same call. Two names so a call site says which wire
// mode it gets instead of passing a bare boolean.
func (c *anthropicClient) post(ctx context.Context, req model.Request) (io.ReadCloser, error) {
	return c.send(ctx, req, false)
}

func (c *anthropicClient) postStream(ctx context.Context, req model.Request) (io.ReadCloser, error) {
	return c.send(ctx, req, true)
}

func (c *anthropicClient) send(ctx context.Context, req model.Request, stream bool) (io.ReadCloser, error) {
	body, status, err := c.sendOnce(ctx, req, stream)
	if err != nil && status == http.StatusBadRequest && len(req.ResponseSchema) > 0 {
		// ResponseSchema is a best-effort generation guardrail (see
		// model.Request.ResponseSchema): not every Anthropic model supports
		// output_config.format, and one that doesn't rejects it with a 400.
		// Rather than fail the whole call, retry with the schema cleared — the
		// caller's parse→validate→retry policy and the evidence gate remain the
		// authority, exactly as for a provider that ignores the schema outright.
		// A 400 with an unrelated cause simply recurs on the retry and surfaces
		// then. The clear is on a copy; the caller's request is untouched.
		unconstrained := req
		unconstrained.ResponseSchema = nil
		body, _, err = c.sendOnce(ctx, unconstrained, stream)
	}
	return body, err
}

// sendOnce performs one Messages call, attaching the output_config.format
// guardrail when the request carries a schema. The returned status is the HTTP
// status (0 on a transport-level failure) so send can distinguish a
// schema-rejection 400 from a transport error.
func (c *anthropicClient) sendOnce(ctx context.Context, req model.Request, stream bool) (io.ReadCloser, int, error) {
	// Anthropic is natively capable of image/document blocks; mapping them is a
	// cheap follow-up. Phase-1 ships the honest reject-guard so the uniform
	// Attachments field can never silently drop (spec §3.8, "the guard is the floor").
	if err := attachmentUnsupported("anthropic", req.Attachments, rejectAllAttachments); err != nil {
		return nil, 0, err
	}
	wire := anthropicWire{
		Model:     req.Model,
		MaxTokens: req.MaxTokens,
		System:    req.System,
		Messages:  wireMessages("", req.Messages),
		Stream:    stream,
	}
	if wire.Model == "" {
		wire.Model = c.defaultModel
	}
	if wire.MaxTokens <= 0 {
		wire.MaxTokens = anthropicMaxTokensDefault
	}
	for _, tool := range req.Tools {
		wire.Tools = append(wire.Tools, anthropicToolWire{
			Name: tool.Name, Description: tool.Description, InputSchema: tool.InputSchema,
		})
	}
	if len(req.ResponseSchema) > 0 {
		wire.OutputConfig = &anthropicOutputConfig{
			Format: &anthropicResponseFormat{Type: jsonSchemaFormatType, Schema: req.ResponseSchema},
		}
	}
	payload, _, err := sendablePayload(ctx, wire, req.SecretStripper)
	if err != nil {
		return nil, 0, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/messages", bytes.NewReader(payload))
	if err != nil {
		return nil, 0, fmt.Errorf("ai: anthropic: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Api-Key", c.apiKey)
	httpReq.Header.Set("Anthropic-Version", anthropicAPIVersion)
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, 0, fmt.Errorf("ai: anthropic: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		//craft:ignore swallowed-errors best-effort close on the error path — the API status error is the answer
		defer func() { _ = resp.Body.Close() }()
		return nil, resp.StatusCode, anthropicError(resp)
	}
	return resp.Body, resp.StatusCode, nil
}

// anthropicError surfaces the API's error type and message — and only
// those, so a logged failure can never echo the request (or the key).
func anthropicError(resp *http.Response) error {
	var apiErr struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if readErr == nil && json.Unmarshal(raw, &apiErr) == nil && apiErr.Error.Type != "" {
		return fmt.Errorf("ai: anthropic: %s: %s (http %d)", apiErr.Error.Type, apiErr.Error.Message, resp.StatusCode)
	}
	return fmt.Errorf("ai: anthropic: http %d", resp.StatusCode)
}

// anthropicStream parses the Messages SSE stream, yielding text deltas.
type anthropicStream struct {
	body    io.ReadCloser
	scanner *bufio.Scanner
}

func (s *anthropicStream) Next(ctx context.Context) (string, bool, error) {
	for s.scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return "", false, err
		}
		line := s.scanner.Text()
		data, isData := strings.CutPrefix(line, "data: ")
		if !isData {
			continue
		}
		var ev struct {
			Type  string `json:"type"`
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
		}
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return "", false, fmt.Errorf("ai: anthropic: stream event: %w", err)
		}
		switch {
		case ev.Type == "content_block_delta" && ev.Delta.Type == "text_delta":
			return ev.Delta.Text, true, nil
		case ev.Type == "message_stop":
			return "", false, nil
		}
	}
	if err := s.scanner.Err(); err != nil {
		return "", false, fmt.Errorf("ai: anthropic: stream: %w", err)
	}
	return "", false, nil
}

func (s *anthropicStream) Close() error { return s.body.Close() }
