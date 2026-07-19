// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// openaiClient is the native OpenAI adapter (BYOK, ADR-0020) speaking the
// Responses API (POST /v1/responses) rather than the generic
// /v1/chat/completions the openai_compatible transport uses. The Responses
// wire is what carries native reasoning (reasoning.effort), itemized usage
// (cached + reasoning tokens), and typed image/file input parts — none of
// which the chat-completions shape expresses. stdlib HTTP only, mirroring
// anthropic.go; no vendor SDK.
type openaiClient struct {
	http         *http.Client
	baseURL      string
	apiKey       string
	defaultModel string
}

// openaiMaxOutputDefault caps a request that didn't set MaxTokens, so a caller
// bug can't turn into unbounded spend (mirrors the Anthropic default).
const openaiMaxOutputDefault = 1024

type openaiWire struct {
	Model           string            `json:"model"`
	Input           []openaiInputItem `json:"input"`
	MaxOutputTokens int               `json:"max_output_tokens,omitempty"`
	Text            *openaiText       `json:"text,omitempty"`
	Reasoning       *openaiReasoning  `json:"reasoning,omitempty"`
	Stream          bool              `json:"stream,omitempty"`
	// Store is pinned false (no omitempty — the field must be on the wire):
	// the Responses API defaults to store:true, which retains prompts — CRM
	// record content — server-side for ~30 days. BYOK egress sends the
	// request for inference only, never for vendor-side retention.
	Store bool `json:"store"`
}

type openaiInputItem struct {
	Role    string            `json:"role"`
	Content []openaiInputPart `json:"content"`
}

// openaiInputPart is one content part. Only the fields relevant to the part's
// Type are populated; the rest are omitted so the wire stays minimal.
type openaiInputPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
	FileData string `json:"file_data,omitempty"`
	FileName string `json:"filename,omitempty"`
	FileURL  string `json:"file_url,omitempty"`
	FileID   string `json:"file_id,omitempty"`
}

type openaiText struct {
	Format openaiResponseFormat `json:"format"`
}

// openaiResponseFormat is the Responses API structured-output shape: the
// json_schema descriptor sits directly under text.format (siblings, no
// json_schema:{} wrapper). strict:true — OpenAI guarantees conformance, so
// unlike the lenient openai_compatible strict:false, this enforces the schema.
type openaiResponseFormat struct {
	Type   string          `json:"type"`
	Name   string          `json:"name"`
	Schema json.RawMessage `json:"schema"`
	Strict bool            `json:"strict"`
}

type openaiReasoning struct {
	Effort string `json:"effort"`
}

// openaiOptions is the vendor-only knob namespace read from
// Request.ProviderOptions["openai"].
type openaiOptions struct {
	ReasoningEffort string `json:"reasoning_effort"`
}

type openaiResponse struct {
	ID string `json:"id"`
	// Model is the Responses API's served-identity field: the specific model
	// that generated this response.
	Model string `json:"model"`
	// Status is the terminal response state: "completed" is the only success;
	// "failed" carries Error, "incomplete" carries IncompleteDetails (e.g.
	// max_output_tokens, content_filter). Anything else must surface as an
	// error — a truncated or filtered answer must never read as a clean one.
	Status string `json:"status"`
	Error  struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	IncompleteDetails struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Output []struct {
		Type    string `json:"type"`
		Content []struct {
			Type    string `json:"type"`
			Text    string `json:"text"`
			Refusal string `json:"refusal"`
		} `json:"content"`
	} `json:"output"`
	Usage struct {
		InputTokens       int `json:"input_tokens"`
		OutputTokens      int `json:"output_tokens"`
		InputTokenDetails struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"input_tokens_details"`
		OutputTokenDetails struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		} `json:"output_tokens_details"`
	} `json:"usage"`
}

func (c *openaiClient) Complete(ctx context.Context, req model.Request) (model.Response, error) {
	body, err := c.post(ctx, "/v1/responses", req, false)
	if err != nil {
		return model.Response{}, err
	}
	//craft:ignore swallowed-errors best-effort close of a response body already read to completion — the decode result decides the outcome
	defer func() { _ = body.Close() }()
	var out openaiResponse
	if err := json.NewDecoder(body).Decode(&out); err != nil {
		return model.Response{}, fmt.Errorf("ai: openai: decode response: %w", err)
	}
	if err := openaiTerminalStatus(out); err != nil {
		return model.Response{}, err
	}
	// Walk output[]: a type:"reasoning" item can precede the message, and a
	// type:"refusal" part is a first-class outcome — never output[0].content[0].
	var text strings.Builder
	for _, item := range out.Output {
		if item.Type != "message" {
			continue
		}
		for _, part := range item.Content {
			switch part.Type {
			case "output_text":
				text.WriteString(part.Text)
			case "refusal":
				return model.Response{}, fmt.Errorf("ai: openai: model refusal: %s", part.Refusal)
			}
		}
	}
	resp := model.Response{
		Text:            text.String(),
		InputTokens:     out.Usage.InputTokens,
		OutputTokens:    out.Usage.OutputTokens,
		CachedTokens:    out.Usage.InputTokenDetails.CachedTokens,
		ReasoningTokens: out.Usage.OutputTokenDetails.ReasoningTokens,
		ServedModel:     out.Model,
	}
	if out.ID != "" {
		if meta, err := json.Marshal(map[string]string{"response_id": out.ID}); err == nil {
			resp.ProviderMetadata = map[string]json.RawMessage{"openai": meta}
		}
	}
	return resp, nil
}

//nolint:ireturn // model.Client.Stream returns the port's TokenStream interface by contract
func (c *openaiClient) Stream(ctx context.Context, req model.Request) (model.TokenStream, error) {
	body, err := c.post(ctx, "/v1/responses", req, true)
	if err != nil {
		return nil, err
	}
	return &openaiStream{body: body, scanner: streamLineScanner(body)}, nil
}

func (c *openaiClient) Embed(ctx context.Context, req model.EmbedRequest) (model.Embeddings, error) {
	return openAIWireEmbed(ctx, c.postRaw, c.defaultModel, req)
}

func (c *openaiClient) Caps() model.Capabilities {
	return model.Capabilities{Streaming: true, EmbedDims: 0, LocalOnly: false}
}

func (c *openaiClient) post(ctx context.Context, path string, req model.Request, stream bool) (io.ReadCloser, error) {
	// OpenAI carries image and PDF/file parts natively; reject any other MIME
	// rather than silently drop it (spec §3.8).
	if err := attachmentUnsupported("openai", req.Attachments, func(m string) bool { return isImage(m) || m == "application/pdf" }); err != nil {
		return nil, err
	}
	// Native Responses-API tool mapping is a follow-up; reject tools rather than
	// silently drop them (the tasks run in JSON mode today, so none are passed).
	if len(req.Tools) > 0 {
		return nil, fmt.Errorf("ai: openai: native tool-use is not implemented yet (request set %d tool(s))", len(req.Tools))
	}
	wire := openaiWire{Model: req.Model, MaxOutputTokens: req.MaxTokens, Stream: stream}
	if wire.Model == "" {
		wire.Model = c.defaultModel
	}
	if wire.MaxOutputTokens <= 0 {
		wire.MaxOutputTokens = openaiMaxOutputDefault
	}
	wire.Input = openaiInputMessages(req.System, req.Messages, req.Attachments)
	if len(req.ResponseSchema) > 0 {
		wire.Text = &openaiText{Format: openaiResponseFormat{
			Type: jsonSchemaFormatType, Name: openAICompatSchemaName, Schema: req.ResponseSchema, Strict: true,
		}}
	}
	effort, err := openaiReasoningEffort(req.ProviderOptions)
	if err != nil {
		return nil, err
	}
	if effort != "" {
		wire.Reasoning = &openaiReasoning{Effort: effort}
	}
	payload, _, err := sendablePayload(ctx, wire, req.SecretStripper)
	if err != nil {
		return nil, err
	}
	return c.postRaw(ctx, path, payload)
}

// openaiInputMessages builds the Responses `input` array: system as a leading
// message, each turn's text as an input_text/output_text part, and every
// attachment appended to the final user turn's content.
func openaiInputMessages(system string, msgs []model.Message, atts []model.Attachment) []openaiInputItem {
	items := make([]openaiInputItem, 0, len(msgs)+1)
	if system != "" {
		items = append(items, openaiInputItem{Role: roleSystem, Content: []openaiInputPart{{Type: "input_text", Text: system}}})
	}
	for _, m := range msgs {
		partType := "input_text"
		if m.Role == roleAssistant {
			partType = "output_text"
		}
		items = append(items, openaiInputItem{Role: m.Role, Content: []openaiInputPart{{Type: partType, Text: m.Content}}})
	}
	if len(atts) > 0 {
		items = attachToLastUserTurn(items, atts)
	}
	return items
}

// attachToLastUserTurn appends attachment parts to the last user-role item,
// adding a user item if none exists — attachments belong to a user turn.
func attachToLastUserTurn(items []openaiInputItem, atts []model.Attachment) []openaiInputItem {
	idx := -1
	for i := range items {
		if items[i].Role == roleUser {
			idx = i
		}
	}
	if idx == -1 {
		items = append(items, openaiInputItem{Role: roleUser})
		idx = len(items) - 1
	}
	for _, a := range atts {
		items[idx].Content = append(items[idx].Content, openaiAttachmentPart(a))
	}
	return items
}

func openaiAttachmentPart(a model.Attachment) openaiInputPart {
	if isImage(a.MIME) {
		part := openaiInputPart{Type: "input_image"}
		if a.URI != "" {
			part.ImageURL = a.URI
		} else {
			part.ImageURL = dataURI(a.MIME, a.Bytes)
		}
		return part
	}
	// application/pdf (the only other allowed MIME, gated in post). A URI is
	// either a public URL (file_url) or an OpenAI file handle (file_id); inline
	// bytes ride file_data.
	part := openaiInputPart{Type: "input_file", FileName: a.Name}
	switch {
	case a.URI == "":
		part.FileData = dataURI(a.MIME, a.Bytes)
	case strings.HasPrefix(a.URI, "http://"), strings.HasPrefix(a.URI, "https://"):
		part.FileURL = a.URI
	default:
		part.FileID = a.URI
	}
	return part
}

func dataURI(mime string, raw []byte) string {
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(raw)
}

func openaiReasoningEffort(opts map[string]json.RawMessage) (string, error) {
	raw, ok := opts["openai"]
	if !ok {
		return "", nil
	}
	var o openaiOptions
	if err := json.Unmarshal(raw, &o); err != nil {
		return "", fmt.Errorf("ai: openai: provider options: %w", err)
	}
	return o.ReasoningEffort, nil
}

func (c *openaiClient) postRaw(ctx context.Context, path string, payload []byte) (io.ReadCloser, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("ai: openai: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ai: openai: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		//craft:ignore swallowed-errors best-effort close on the error path — the API status error is the answer
		defer func() { _ = resp.Body.Close() }()
		return nil, openaiError(resp)
	}
	return resp.Body, nil
}

// openaiError surfaces the API's error type and message — and only those, so a
// logged failure can never echo the request (or the key).
func openaiError(resp *http.Response) error {
	var apiErr struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if readErr == nil && json.Unmarshal(raw, &apiErr) == nil && apiErr.Error.Type != "" {
		return fmt.Errorf("ai: openai: %s: %s (http %d)", apiErr.Error.Type, apiErr.Error.Message, resp.StatusCode)
	}
	return fmt.Errorf("ai: openai: http %d", resp.StatusCode)
}

// openaiTerminalStatus maps a non-completed Responses object to an error: a
// failed call carries the API's error, an incomplete one names why generation
// stopped (max_output_tokens, content_filter), and a missing status means the
// body was not a terminal Responses object at all. Any of them read as a clean
// answer would silently hand the caller a truncated or filtered result —
// "completed" is the only success.
func openaiTerminalStatus(out openaiResponse) error {
	switch out.Status {
	case "completed":
		return nil
	case "failed":
		return fmt.Errorf("ai: openai: response failed: %s: %s", out.Error.Code, out.Error.Message)
	case "incomplete":
		return fmt.Errorf("ai: openai: response incomplete: %s", out.IncompleteDetails.Reason)
	case "":
		return fmt.Errorf("ai: openai: response carries no terminal status")
	default:
		return fmt.Errorf("ai: openai: response ended with status %q", out.Status)
	}
}

// openaiStream parses the Responses SSE stream, yielding text deltas from
// response.output_text.delta events. response.completed is the ONLY clean
// terminal — failed/incomplete/error events surface as errors, never as EOF.
type openaiStream struct {
	body    io.ReadCloser
	scanner *bufio.Scanner
}

func (s *openaiStream) Next(ctx context.Context) (string, bool, error) {
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
			Type     string         `json:"type"`
			Delta    string         `json:"delta"`
			Response openaiResponse `json:"response"`
			// Code/Message are the top-level `error` event's shape.
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return "", false, fmt.Errorf("ai: openai: stream event: %w", err)
		}
		switch ev.Type {
		case "response.output_text.delta":
			if ev.Delta != "" {
				return ev.Delta, true, nil
			}
		case "response.completed":
			return "", false, nil
		case "response.failed", "response.incomplete":
			if err := openaiTerminalStatus(ev.Response); err != nil {
				return "", false, err
			}
			// The embedded response object omitted its status — the event
			// type itself is still the authority that this is a failure.
			return "", false, fmt.Errorf("ai: openai: stream ended with %s", ev.Type)
		case "error":
			return "", false, fmt.Errorf("ai: openai: stream error: %s: %s", ev.Code, ev.Message)
		}
	}
	if err := s.scanner.Err(); err != nil {
		return "", false, fmt.Errorf("ai: openai: stream: %w", err)
	}
	// EOF without response.completed: the connection dropped mid-generation.
	return "", false, fmt.Errorf("ai: openai: stream ended without a terminal event")
}

func (s *openaiStream) Close() error { return s.body.Close() }
