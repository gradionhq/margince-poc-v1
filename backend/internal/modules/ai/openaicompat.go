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
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

type openAICompatClient struct {
	http         *http.Client
	baseURL      string
	apiKey       string // "" ⇒ send no Authorization header (local vLLM)
	localOnly    bool   // Caps().LocalOnly — the sovereign-eligibility bit
	defaultModel string
	// attachmentMIMEs is what THIS binding carries, translated from the routing
	// config's `input:` (inputmodality.go). Every other adapter answers that from
	// a constant because its wire decides; here the served model decides, and
	// only the operator can see which model that is. Nil ⇒ text-only, the default
	// for an undeclared binding.
	//
	// One field, two uses — Caps() advertises it and sendChat enforces it — so a
	// binding cannot advertise a media type its own wire then refuses.
	attachmentMIMEs []string
}

// openAICompatSchemaName labels the structured-output schema; OpenAI's
// response_format requires a name, and the value is otherwise opaque.
const openAICompatSchemaName = "structured_output"

type openAICompatChatWire struct {
	Model string `json:"model"`
	// Messages is this adapter's own message type rather than the wireMessage
	// Ollama shares: only here can a turn carry attachment parts, and widening
	// the shared type would change Ollama's wire to buy nothing.
	Messages  []openAICompatMessage `json:"messages"`
	Tools     []ollamaToolWire      `json:"tools,omitempty"`
	MaxTokens int                   `json:"max_tokens,omitempty"`
	Stream    bool                  `json:"stream"`
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
	// Model is the wire's echoed model field: this generic surface merely
	// reflects back the requested model id rather than confirming what
	// actually generated the completion (unlike the native adapters' own
	// served-identity fields) — the router's servedSource map tags it "echo"
	// accordingly, never "response".
	Model   string `json:"model"`
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
		ServedModel:  out.Model,
	}, nil
}

//nolint:ireturn // model.Client.Stream returns the port's TokenStream interface by contract
func (c *openAICompatClient) Stream(ctx context.Context, req model.Request) (model.TokenStream, error) {
	body, err := c.sendChat(ctx, req, true)
	if err != nil {
		return nil, err
	}
	return &openAICompatStream{body: body, scanner: streamLineScanner(body)}, nil
}

func (c *openAICompatClient) Embed(ctx context.Context, req model.EmbedRequest) (model.Embeddings, error) {
	// `dimensions` is OpenAI's matryoshka-truncation knob; the generic wire
	// gives no way to know whether the server honors it, and vLLM rejects it
	// outright on models that aren't MRL-trained (a 400 on every embed call).
	// Omit it — the store's width check catches a genuinely mismatched model.
	req.Dimensions = 0
	return openAIWireEmbed(ctx, c.post, c.defaultModel, req)
}

// carriesNothing is the carriage declaration of an adapter whose wire has no
// attachment parts at all. Named rather than written as a bare nil at each
// site, so "this wire is text-only" reads as a decision instead of an omission.
var carriesNothing []string

// carriesImagesAndPDF is the carriage declaration shared by the two adapters
// whose wires take document parts natively.
var carriesImagesAndPDF = []string{"image/*", "application/pdf"}

// carriesImages is the declaration of an adapter whose wire takes images and
// nothing else. Anthropic and Ollama both spell an image part uniformly — one
// `image` block, one `images` array — while a PDF is model-dependent on the
// first wire and absent from the second, so the honest declaration stops at
// images rather than claiming a document lane one bound model may not serve.
var carriesImages = []string{"image/*"}

// DocumentMIMEs is every media type some adapter in this build carries as an
// input part. It answers "could any binding have been handed this", which is a
// different question from "will THIS binding take it" (that is Caps()) — the
// certification corpus asks the first, because a fixture pinning a media type
// no adapter accepts describes a call this build cannot make.
//
// Derived from the adapters whose carriage is fixed in code, which is the whole
// answer while `image` — the only modality a binding may declare — is a subset
// of what those adapters already carry. A modality that widens PAST this set
// (audio, say) would have to widen this too, or the corpus would reject a
// fixture some binding could in fact be handed.
func DocumentMIMEs() []string { return slices.Clone(carriesImagesAndPDF) }

// isImage selects which KIND of wire part an accepted attachment becomes, once
// carriage has already been decided. It is not a carriage check — that is
// CarriesMIME over the adapter's declaration — and using it as one is how the
// two answers drift apart.
func isImage(mime string) bool { return strings.HasPrefix(mime, "image/") }

// isFetchableURL reports whether an attachment's URI is a URL the vendor can
// fetch for itself, as opposed to a handle scoped to some provider's own file
// registry. Every adapter that takes a URI has to answer this — openai to pick
// file_url over file_id, anthropic to decide whether it can send the part at
// all — so the two answer it the same way.
//
// Parsed rather than prefix-matched, because both mistakes a prefix makes are
// silent: a bare "https://" has no host to fetch and would go out as a URL, and
// a scheme in capitals — which URLs are case-insensitive in — would be handed to
// a file registry as if it were a handle.
func isFetchableURL(uri string) bool {
	parsed, err := url.Parse(uri)
	if err != nil {
		return false
	}
	scheme := strings.ToLower(parsed.Scheme)
	return (scheme == "https" || scheme == "http") && parsed.Host != ""
}

func (c *openAICompatClient) Caps() model.Capabilities {
	// EmbedDims stays 0 (unknown): the width is a property of whichever
	// model the deployment serves, discovered from the first Embed call.
	// LocalOnly is the provider's trust posture (vllm true, openai_compatible
	// false), fixed at construction — never a wire-visible property.
	// AttachmentMIMEs is the binding's declaration, not a constant: this one
	// adapter serves whatever model the operator pointed it at, so the answer
	// lives in the routing config and arrives at construction.
	return model.Capabilities{
		Streaming:       true,
		EmbedDims:       0,
		LocalOnly:       c.localOnly,
		AttachmentMIMEs: c.attachmentMIMEs,
	}
}

// attachmentUnsupported returns ErrAttachmentUnsupported (provider-tagged) for
// any attachment outside the adapter's declared carriage; nil otherwise. The
// map-or-reject invariant (spec §3.8): no adapter may silently drop an
// attachment.
//
// It takes the SAME declaration the adapter reports from Caps(), not a
// separately-written predicate, so a binding cannot advertise a media type its
// wire then refuses — the two halves are one list.
func attachmentUnsupported(provider string, atts []model.Attachment, declared []string) error {
	for _, a := range atts {
		if !model.CarriesMIME(declared, a.MIME) {
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

// errUnfetchableAttachmentURI refuses an attachment whose MIME the wire carries
// but whose URI it cannot resolve — a vendor file handle on an endpoint with no
// such registry, or a URL on a wire that takes inline bytes only.
//
// It is the carriage sentinel rather than a bare error because that is what it
// is: this binding cannot be handed this part, and a caller that falls back to
// another lane on ErrAttachmentUnsupported should fall back here too. The URI
// itself is not echoed — it can be a signed URL, and an error message is the
// wrong place for one.
func errUnfetchableAttachmentURI(provider, accepts string) error {
	return fmt.Errorf("ai: %s: this attachment's uri is not one this wire can resolve; it takes %s: %w",
		provider, accepts, model.ErrAttachmentUnsupported)
}

// refuseUnsupportedAttachments applies the map-or-reject invariant (spec §3.8)
// against THIS binding's declaration, and restates the refusal as something the
// operator can act on.
//
// Every other adapter's carriage refusal is final: its wire decides, and there
// is nothing to change. This one's is a config line away from being a success,
// so an error that stops at "cannot carry" sends the reader into the adapter's
// source to find out why a capable model was refused.
func (c *openAICompatClient) refuseUnsupportedAttachments(atts []model.Attachment) error {
	err := attachmentUnsupported("openai-compat", atts, c.attachmentMIMEs)
	if !errors.Is(err, model.ErrAttachmentUnsupported) {
		return err // nil, or the Bytes-XOR-URI fault, which no config line fixes
	}
	return fmt.Errorf("%w; this binding carries %s — set `input:` on its tier in the routing config to what the bound model accepts",
		err, describeCarriage(c.attachmentMIMEs))
}

// describeCarriage renders a carriage set for an error message, naming the empty
// case rather than printing "[]" at an operator.
func describeCarriage(declared []string) string {
	if len(declared) == 0 {
		return "no attachments"
	}
	return strings.Join(declared, ", ")
}

func (c *openAICompatClient) sendChat(ctx context.Context, req model.Request, stream bool) (io.ReadCloser, error) {
	// Carriage on this wire is the BINDING's declaration, not the adapter's: one
	// client serves whichever model the operator bound, and only images are
	// spelled uniformly enough here to be declarable (ai-operational-spec §1.4).
	if err := c.refuseUnsupportedAttachments(req.Attachments); err != nil {
		return nil, err
	}
	wire := openAICompatChatWire{Model: req.Model, Stream: stream, MaxTokens: req.MaxTokens}
	if wire.Model == "" {
		wire.Model = c.defaultModel
	}
	wire.Messages = openAICompatMessages(req.System, req.Messages, req.Attachments)
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

// openAICompatError surfaces the vendor's structured error message only —
// never the raw response body, which may be unstructured HTML/text — so a logged
// failure can't echo the request or leak provider internals (the anthropic /
// openai pattern). Two structured shapes exist on this generic wire: OpenAI's
// nested {"error":{type,message}} and vLLM's top-level {"object":"error",
// type, message}; a body that can't be read falls back to the HTTP status.
func openAICompatError(resp *http.Response) error {
	var apiErr struct {
		Type    string `json:"type"`
		Message string `json:"message"`
		Error   struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if readErr == nil && json.Unmarshal(raw, &apiErr) == nil {
		if apiErr.Error.Message != "" {
			return fmt.Errorf("ai: openai-compat: %s: %s (http %d)", apiErr.Error.Type, apiErr.Error.Message, resp.StatusCode)
		}
		if apiErr.Message != "" {
			return fmt.Errorf("ai: openai-compat: %s: %s (http %d)", apiErr.Type, apiErr.Message, resp.StatusCode)
		}
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
