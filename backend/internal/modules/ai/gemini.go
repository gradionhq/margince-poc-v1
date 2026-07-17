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

// geminiClient is the native Google Gemini adapter (BYOK, ADR-0020) speaking
// the v1beta generateContent surface — not the OpenAI-compat layer, which
// cannot reach the Files API for document input. The native wire is what
// carries attachments (inlineData/fileData), thinking control
// (thinkingConfig.thinkingLevel), full-JSON-Schema structured output
// (responseJsonSchema), and the thought-signature continuity channel. stdlib
// HTTP only, mirroring anthropic.go; no vendor SDK. Field tags are camelCase to
// match Google's wire (see the //nolint:tagliatelle markers).
type geminiClient struct {
	http         *http.Client
	baseURL      string
	apiKey       string
	defaultModel string
}

// geminiMaxOutputDefault caps a request that didn't set MaxTokens.
const geminiMaxOutputDefault = 1024

// geminiEmbedModel is Gemini's dedicated embedding model; the chat model id
// does not serve :embedContent.
const geminiEmbedModel = "gemini-embedding-001"

type geminiWire struct {
	Contents          []geminiContent  `json:"contents"`
	SystemInstruction *geminiContent   `json:"systemInstruction,omitempty"` //nolint:tagliatelle // Google's wire format (camelCase)
	GenerationConfig  *geminiGenConfig `json:"generationConfig,omitempty"`  //nolint:tagliatelle // Google's wire format (camelCase)
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

// geminiPart is one content part; only the field relevant to the part kind is
// populated. ThoughtSignature rides the model turn for reasoning continuity.
type geminiPart struct {
	Text             string            `json:"text,omitempty"`
	InlineData       *geminiInlineData `json:"inlineData,omitempty"`       //nolint:tagliatelle // Google's wire format (camelCase)
	FileData         *geminiFileData   `json:"fileData,omitempty"`         //nolint:tagliatelle // Google's wire format (camelCase)
	ThoughtSignature string            `json:"thoughtSignature,omitempty"` //nolint:tagliatelle // Google's wire format (camelCase)
}

type geminiInlineData struct {
	MimeType string `json:"mimeType"` //nolint:tagliatelle // Google's wire format (camelCase)
	Data     string `json:"data"`
}

type geminiFileData struct {
	MimeType string `json:"mimeType"` //nolint:tagliatelle // Google's wire format (camelCase)
	FileURI  string `json:"fileUri"`  //nolint:tagliatelle // Google's wire format (camelCase)
}

// geminiGenConfig carries structured-output and thinking controls. Structured
// output uses responseJsonSchema (the full-JSON-Schema field) — NOT the older
// OpenAPI-subset responseSchema — with responseMimeType application/json.
type geminiGenConfig struct {
	MaxOutputTokens    int             `json:"maxOutputTokens,omitempty"`    //nolint:tagliatelle // Google's wire format (camelCase)
	ResponseMimeType   string          `json:"responseMimeType,omitempty"`   //nolint:tagliatelle // Google's wire format (camelCase)
	ResponseJSONSchema json.RawMessage `json:"responseJsonSchema,omitempty"` //nolint:tagliatelle // Google's wire format (camelCase)
	ThinkingConfig     *geminiThinking `json:"thinkingConfig,omitempty"`     //nolint:tagliatelle // Google's wire format (camelCase)
}

type geminiThinking struct {
	ThinkingLevel string `json:"thinkingLevel"` //nolint:tagliatelle // Google's wire format (camelCase)
}

// geminiEmbedWire is the :embedContent request body — a single content whose
// parts carry the text to embed.
type geminiEmbedWire struct {
	Model   string        `json:"model"`
	Content geminiContent `json:"content"`
}

// geminiOptions is the vendor-only knob namespace read from
// Request.ProviderOptions["gemini"].
type geminiOptions struct {
	ThinkingLevel     string   `json:"thinking_level"`
	ThoughtSignatures []string `json:"thought_signatures"`
}

type geminiResponse struct {
	Candidates []struct {
		Content geminiContent `json:"content"`
	} `json:"candidates"`
	UsageMetadata struct {
		PromptTokenCount        int `json:"promptTokenCount"`        //nolint:tagliatelle // Google's wire format (camelCase)
		CandidatesTokenCount    int `json:"candidatesTokenCount"`    //nolint:tagliatelle // Google's wire format (camelCase)
		CachedContentTokenCount int `json:"cachedContentTokenCount"` //nolint:tagliatelle // Google's wire format (camelCase)
		ThoughtsTokenCount      int `json:"thoughtsTokenCount"`      //nolint:tagliatelle // Google's wire format (camelCase)
	} `json:"usageMetadata"` //nolint:tagliatelle // Google's wire format (camelCase)
}

func (c *geminiClient) Complete(ctx context.Context, req model.Request) (model.Response, error) {
	body, err := c.generate(ctx, req, false)
	if err != nil {
		return model.Response{}, err
	}
	//craft:ignore swallowed-errors best-effort close of a response body already read to completion — the decode result decides the outcome
	defer func() { _ = body.Close() }()
	var out geminiResponse
	if err := json.NewDecoder(body).Decode(&out); err != nil {
		return model.Response{}, fmt.Errorf("ai: gemini: decode response: %w", err)
	}
	var text strings.Builder
	var signatures []string
	for _, cand := range out.Candidates {
		for _, part := range cand.Content.Parts {
			text.WriteString(part.Text)
			if part.ThoughtSignature != "" {
				signatures = append(signatures, part.ThoughtSignature)
			}
		}
	}
	resp := model.Response{
		Text:            text.String(),
		InputTokens:     out.UsageMetadata.PromptTokenCount,
		OutputTokens:    out.UsageMetadata.CandidatesTokenCount,
		CachedTokens:    out.UsageMetadata.CachedContentTokenCount,
		ReasoningTokens: out.UsageMetadata.ThoughtsTokenCount,
	}
	if len(signatures) > 0 {
		meta, _ := json.Marshal(map[string][]string{"thought_signatures": signatures})
		resp.ProviderMetadata = map[string]json.RawMessage{"gemini": meta}
	}
	return resp, nil
}

//nolint:ireturn // model.Client.Stream returns the port's TokenStream interface by contract
func (c *geminiClient) Stream(ctx context.Context, req model.Request) (model.TokenStream, error) {
	body, err := c.generate(ctx, req, true)
	if err != nil {
		return nil, err
	}
	return &geminiStream{body: body, scanner: bufio.NewScanner(body)}, nil
}

func (c *geminiClient) Embed(ctx context.Context, req model.EmbedRequest) (model.Embeddings, error) {
	embedModel := req.Model
	if embedModel == "" {
		embedModel = geminiEmbedModel
	}
	// One :embedContent call per input (spec §3.5's named endpoint). A large
	// retrieval batch is therefore N sequential round-trips; folding onto
	// :batchEmbedContents for a single call is a follow-up.
	vectors := make([][]float32, 0, len(req.Inputs))
	dims := 0
	for _, input := range req.Inputs {
		wire := geminiEmbedWire{
			Model:   "models/" + embedModel,
			Content: geminiContent{Parts: []geminiPart{{Text: input}}},
		}
		payload, _, err := sendablePayload(ctx, wire, nil)
		if err != nil {
			return model.Embeddings{}, err
		}
		body, err := c.post(ctx, "/models/"+embedModel+":embedContent", payload)
		if err != nil {
			return model.Embeddings{}, err
		}
		var out struct {
			Embedding struct {
				Values []float32 `json:"values"`
			} `json:"embedding"`
		}
		decErr := json.NewDecoder(body).Decode(&out)
		//craft:ignore swallowed-errors best-effort close of a response body already read to completion — the decode result decides the outcome
		_ = body.Close()
		if decErr != nil {
			return model.Embeddings{}, fmt.Errorf("ai: gemini: decode embeddings: %w", decErr)
		}
		vectors = append(vectors, out.Embedding.Values)
		if len(out.Embedding.Values) > dims {
			dims = len(out.Embedding.Values)
		}
	}
	return model.Embeddings{Vectors: vectors, Dims: dims}, nil
}

func (c *geminiClient) Caps() model.Capabilities {
	return model.Capabilities{Streaming: true, EmbedDims: 0, LocalOnly: false}
}

func (c *geminiClient) generate(ctx context.Context, req model.Request, stream bool) (io.ReadCloser, error) {
	// Gemini carries image and PDF parts natively; reject any other MIME rather
	// than silently drop it (spec §3.8).
	if err := attachmentUnsupported("gemini", req.Attachments, func(m string) bool { return isImage(m) || m == "application/pdf" }); err != nil {
		return nil, err
	}
	genModel := req.Model
	if genModel == "" {
		genModel = c.defaultModel
	}
	opts := geminiReadOptions(req.ProviderOptions)
	wire := geminiWire{Contents: geminiContents(req.Messages, req.Attachments, opts.ThoughtSignatures)}
	if req.System != "" {
		wire.SystemInstruction = &geminiContent{Parts: []geminiPart{{Text: req.System}}}
	}
	wire.GenerationConfig = geminiGenerationConfig(req, opts)

	method := "generateContent"
	query := ""
	if stream {
		method = "streamGenerateContent"
		query = "?alt=sse"
	}
	payload, _, err := sendablePayload(ctx, wire, req.SecretStripper)
	if err != nil {
		return nil, err
	}
	// Paths are version-relative: the API version (/v1beta) lives in baseURL
	// (defaultGeminiBaseURL), so a proxy override keeps the whole prefix in one place.
	return c.post(ctx, "/models/"+genModel+":"+method+query, payload)
}

// geminiContents maps messages to the native contents array (assistant→model)
// and echoes any thought signatures back onto the model-role parts in order, so
// stateless multi-turn keeps Gemini's reasoning continuity (spec §3.5).
func geminiContents(msgs []model.Message, atts []model.Attachment, signatures []string) []geminiContent {
	contents := make([]geminiContent, 0, len(msgs))
	sigIdx := 0
	for _, m := range msgs {
		role := m.Role
		if role == roleAssistant {
			role = roleModel
		}
		part := geminiPart{Text: m.Content}
		if role == roleModel && sigIdx < len(signatures) {
			part.ThoughtSignature = signatures[sigIdx]
			sigIdx++
		}
		contents = append(contents, geminiContent{Role: role, Parts: []geminiPart{part}})
	}
	if len(atts) > 0 {
		contents = geminiAttachToLastUserTurn(contents, atts)
	}
	return contents
}

func geminiAttachToLastUserTurn(contents []geminiContent, atts []model.Attachment) []geminiContent {
	idx := -1
	for i := range contents {
		if contents[i].Role == roleUser {
			idx = i
		}
	}
	if idx == -1 {
		contents = append(contents, geminiContent{Role: roleUser})
		idx = len(contents) - 1
	}
	for _, a := range atts {
		contents[idx].Parts = append(contents[idx].Parts, geminiAttachmentPart(a))
	}
	return contents
}

func geminiAttachmentPart(a model.Attachment) geminiPart {
	if a.URI != "" {
		return geminiPart{FileData: &geminiFileData{MimeType: a.MIME, FileURI: a.URI}}
	}
	return geminiPart{InlineData: &geminiInlineData{MimeType: a.MIME, Data: base64.StdEncoding.EncodeToString(a.Bytes)}}
}

func geminiGenerationConfig(req model.Request, opts geminiOptions) *geminiGenConfig {
	cfg := &geminiGenConfig{}
	if req.MaxTokens > 0 {
		cfg.MaxOutputTokens = req.MaxTokens
	} else {
		cfg.MaxOutputTokens = geminiMaxOutputDefault
	}
	if len(req.ResponseSchema) > 0 {
		cfg.ResponseMimeType = "application/json"
		cfg.ResponseJSONSchema = req.ResponseSchema
	}
	if opts.ThinkingLevel != "" {
		cfg.ThinkingConfig = &geminiThinking{ThinkingLevel: opts.ThinkingLevel}
	}
	return cfg
}

func geminiReadOptions(opts map[string]json.RawMessage) geminiOptions {
	raw, ok := opts["gemini"]
	if !ok {
		return geminiOptions{}
	}
	var o geminiOptions
	if err := json.Unmarshal(raw, &o); err != nil {
		return geminiOptions{}
	}
	return o
}

func (c *geminiClient) post(ctx context.Context, path string, payload []byte) (io.ReadCloser, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("ai: gemini: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-goog-api-key", c.apiKey)
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ai: gemini: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		//craft:ignore swallowed-errors best-effort close on the error path — the API status error is the answer
		defer func() { _ = resp.Body.Close() }()
		return nil, geminiError(resp)
	}
	return resp.Body, nil
}

// geminiError surfaces the API's error status and message only, so a logged
// failure can never echo the request (or the key).
func geminiError(resp *http.Response) error {
	var apiErr struct {
		Error struct {
			Status  string `json:"status"`
			Message string `json:"message"`
		} `json:"error"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if json.Unmarshal(raw, &apiErr) == nil && apiErr.Error.Status != "" {
		return fmt.Errorf("ai: gemini: %s: %s (http %d)", apiErr.Error.Status, apiErr.Error.Message, resp.StatusCode)
	}
	return fmt.Errorf("ai: gemini: http %d", resp.StatusCode)
}

// geminiStream reads the :streamGenerateContent?alt=sse stream. There is no
// [DONE] sentinel — the stream simply closes; text arrives at
// candidates[0].content.parts[].text on each chunk.
type geminiStream struct {
	body    io.ReadCloser
	scanner *bufio.Scanner
}

func (s *geminiStream) Next(ctx context.Context) (string, bool, error) {
	for s.scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return "", false, err
		}
		line := s.scanner.Text()
		data, isData := strings.CutPrefix(line, "data: ")
		if !isData {
			continue
		}
		var ev geminiResponse
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return "", false, fmt.Errorf("ai: gemini: stream event: %w", err)
		}
		var chunk strings.Builder
		for _, cand := range ev.Candidates {
			for _, part := range cand.Content.Parts {
				chunk.WriteString(part.Text)
			}
		}
		if chunk.Len() > 0 {
			return chunk.String(), true, nil
		}
	}
	if err := s.scanner.Err(); err != nil {
		return "", false, fmt.Errorf("ai: gemini: stream: %w", err)
	}
	return "", false, nil
}

func (s *geminiStream) Close() error { return s.body.Close() }
