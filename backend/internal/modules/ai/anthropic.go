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
	Model     string              `json:"model"`
	MaxTokens int                 `json:"max_tokens"`
	System    string              `json:"system,omitempty"`
	Messages  []wireMessage       `json:"messages"`
	Tools     []anthropicToolWire `json:"tools,omitempty"`
	Stream    bool                `json:"stream,omitempty"`
}

type anthropicToolWire struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

func (c *anthropicClient) Complete(ctx context.Context, req model.Request) (model.Response, error) {
	body, err := c.post(ctx, req)
	if err != nil {
		return model.Response{}, err
	}
	//craft:ignore swallowed-errors best-effort close of a response body already read to completion — the decode result decides the outcome
	defer func() { _ = body.Close() }()
	var out struct {
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
	}, nil
}

func (c *anthropicClient) Stream(ctx context.Context, req model.Request) (model.TokenStream, error) {
	body, err := c.postStream(ctx, req)
	if err != nil {
		return nil, err
	}
	return &anthropicStream{body: body, scanner: bufio.NewScanner(body)}, nil
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
	payload, _, err := sendablePayload(ctx, wire, req.SecretStripper)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/messages", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("ai: anthropic: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Api-Key", c.apiKey)
	httpReq.Header.Set("Anthropic-Version", anthropicAPIVersion)
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ai: anthropic: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		//craft:ignore swallowed-errors best-effort close on the error path — the API status error is the answer
		defer func() { _ = resp.Body.Close() }()
		return nil, anthropicError(resp)
	}
	return resp.Body, nil
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
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if json.Unmarshal(raw, &apiErr) == nil && apiErr.Error.Type != "" {
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
