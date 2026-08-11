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
	"log/slog"
	"net/http"

	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// ollamaClient is the local/self-host adapter (B-EP06.3): an Ollama
// endpoint on the workspace's own infrastructure. LocalOnly=true makes
// it eligible for the sovereign zero-egress profile — the router
// refuses to bind a sovereign deployment to anything else.
type ollamaClient struct {
	http         *http.Client
	baseURL      string
	defaultModel string
}

type ollamaWire struct {
	Model    string           `json:"model"`
	Messages []wireMessage    `json:"messages"`
	Tools    []ollamaToolWire `json:"tools,omitempty"`
	Stream   bool             `json:"stream"`
	Options  *ollamaOptions   `json:"options,omitempty"`
	// Format constrains decoding to a JSON Schema (Ollama's structured-output
	// mode). Sent only when the request carries a ResponseSchema; omitted
	// otherwise so ordinary free-text calls are unaffected.
	Format json.RawMessage `json:"format,omitempty"`
}

type ollamaOptions struct {
	NumPredict int `json:"num_predict"`
	NumCtx     int `json:"num_ctx"`
}

// ollamaEmbedWire is the /api/embed request. It is the adapter's own rather
// than the shared embedWire because that one is the OpenAI-compatible shape
// (it carries `dimensions`, which Ollama has no parameter for) and because
// options are a runner concept only this provider has.
type ollamaEmbedWire struct {
	Model   string              `json:"model"`
	Input   []string            `json:"input"`
	Options *ollamaEmbedOptions `json:"options,omitempty"`
}

// ollamaEmbedOptions is the embed lane's option set: the window, and nothing
// else. num_predict has no meaning where there is no completion, and sending it
// as zero would state an output budget on a call that produces no output.
type ollamaEmbedOptions struct {
	NumCtx int `json:"num_ctx"`
}

// ollamaMaxTokensDefault caps a request that didn't set MaxTokens, the same
// answer anthropic and gemini give the same gap. The window below is sized from
// this number, so leaving it unset would make the output allowance an accident
// of the arithmetic rather than a stated budget.
const ollamaMaxTokensDefault = 1024

// ollamaContextFloor is Ollama's own default window. The adapter never asks for
// less, so a short request cannot come out worse than saying nothing at all.
const ollamaContextFloor = 4096

// ollamaMaxContext caps the window the adapter will ask for.
//
// The prompt is not ours: the extraction lanes feed this provider the text of
// crawled pages and captured messages, so its LENGTH is chosen by whoever
// published the page or sent the mail. Ollama sizes the runner's KV cache from
// num_ctx when it loads a model, so an uncapped window would let a remote party
// pick that allocation — a megabyte of prose asks for a window in the hundreds
// of thousands of tokens, gigabytes the host must find or fail trying, taking
// every other AI lane in the installation with it.
//
// Clamping buys a better failure: past this point the prompt truncates, which
// is confined to the page that caused it.
const ollamaMaxContext = 32768

// ollamaContextBucket quantizes the window. num_ctx is a RUNNER parameter —
// Ollama reloads the model when a request's value differs from the loaded
// one's — and a byte-derived window is a different number on nearly every call,
// so a crawl fanning out over dozens of pages would pay a model load per page.
// Rounding up keeps a whole workload on one loaded runner, and the remainder to
// the boundary doubles as the headroom the chat template needs.
const ollamaContextBucket = 4096

// ollamaPerMessageOverhead is the role and delimiter scaffolding the template
// wraps around each turn, which a byte count of the content alone cannot see.
const ollamaPerMessageOverhead = 8

// contextWindow sizes `num_ctx` for the assembled request.
//
// num_ctx bounds prompt AND completion together, while num_predict bounds only
// the completion — so asking for more output than the window holds past the
// prompt is a request Ollama cannot satisfy. It does not refuse: it generates
// until the window is full and stops with `done_reason: "length"`.
//
// On a reasoning model that cut lands INSIDE the thinking, which is not the
// content field, so the caller gets a well-formed reply whose content is the
// empty string and a schema-carrying task fails as an unparseable one — a
// wrong-looking model where the truth is a window too small for what was asked.
//
// Measured off the wire rather than the request: wire.Messages already carries
// the system prompt as its leading turn, so counting req.System too would
// double it. The ~4-bytes-per-token heuristic is the one the embed lane meters
// with; it only has to be close, because the floor, the bucket and the cap
// between them decide the value that actually ships.
func (w ollamaWire) contextWindow(maxTokens int) int {
	prompt := len(w.Format)
	for _, message := range w.Messages {
		prompt += len(message.Content) + len(message.Role) + ollamaPerMessageOverhead
	}
	for _, tool := range w.Tools {
		prompt += len(tool.Function.Name) + len(tool.Function.Description) + len(tool.Function.Parameters)
	}
	return ollamaWindowFor(prompt/4 + maxTokens)
}

// ollamaWindowFor rounds a token estimate up to a window this adapter is
// willing to ask for. Both lanes size their own estimate and then come here,
// because the bucket, the floor and the cap are one rule about what the RUNNER
// is asked to allocate rather than anything about prompts or documents — and a
// second copy of it is how the two lanes would drift apart.
func ollamaWindowFor(tokens int) int {
	window := (tokens/ollamaContextBucket + 1) * ollamaContextBucket
	if window < ollamaContextFloor {
		return ollamaContextFloor
	}
	if window > ollamaMaxContext {
		return ollamaMaxContext
	}
	return window
}

// embedContextWindow sizes num_ctx for one /api/embed call, and reports whether
// the longest input still overruns the window it was able to ask for.
//
// Sized off the LONGEST input rather than the sum of the batch: num_ctx is the
// loaded model's per-SEQUENCE window and /api/embed embeds each input
// independently, so summing would ask for a window the work never needed and
// would reach the ceiling on a batch of otherwise ordinary documents.
//
// The estimate is returned alongside the window because this is the one place
// truncation is invisible. A chat request past its window stops generating and
// says done_reason: "length"; an embedding past its window is computed from the
// head of the text and returns a vector of the right width that no caller can
// tell apart from a whole one. And the window ALONE cannot say how much was
// lost — it saturates at the cap, so a document at 33k tokens and one at a
// million both report the same 32768. What was asked for is the half that
// carries the magnitude, so both leave this function.
func embedContextWindow(inputs []string) (window, estimatedTokens int) {
	longest := 0
	for _, input := range inputs {
		if len(input) > longest {
			longest = len(input)
		}
	}
	// The same ~4-bytes-per-token heuristic the chat window and the embed
	// meter both estimate with. It only has to be close: the bucket and the
	// cap decide the value that actually ships.
	estimatedTokens = longest / 4
	return ollamaWindowFor(estimatedTokens), estimatedTokens
}

type ollamaToolWire struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

type ollamaChatEvent struct {
	Model   string `json:"model"`
	Message struct {
		Content string `json:"content"`
	} `json:"message"`
	Done            bool `json:"done"`
	PromptEvalCount int  `json:"prompt_eval_count"`
	EvalCount       int  `json:"eval_count"`
}

func (c *ollamaClient) Complete(ctx context.Context, req model.Request) (model.Response, error) {
	body, err := c.chat(ctx, req)
	if err != nil {
		return model.Response{}, err
	}
	//craft:ignore swallowed-errors best-effort close of a response body already read to completion — the decode result decides the outcome
	defer func() { _ = body.Close() }()
	var out ollamaChatEvent
	if err := json.NewDecoder(body).Decode(&out); err != nil {
		return model.Response{}, fmt.Errorf("ai: ollama: decode response: %w", err)
	}
	return model.Response{
		Text:         out.Message.Content,
		InputTokens:  out.PromptEvalCount,
		OutputTokens: out.EvalCount,
		ServedModel:  out.Model,
	}, nil
}

func (c *ollamaClient) Stream(ctx context.Context, req model.Request) (model.TokenStream, error) {
	body, err := c.chatStream(ctx, req)
	if err != nil {
		return nil, err
	}
	return &ollamaStream{body: body, scanner: streamLineScanner(body)}, nil
}

func (c *ollamaClient) Embed(ctx context.Context, req model.EmbedRequest) (model.Embeddings, error) {
	embedModel := req.Model
	if embedModel == "" {
		embedModel = c.defaultModel
	}
	// Sized for the same reason the chat path is: Ollama runs /api/embed in
	// whatever window the loaded model has, so an unsized call embeds long text
	// from its head — silently, since the vector that comes back is the right
	// width whether or not the model ever saw the end of the document.
	window, estimatedTokens := embedContextWindow(req.Inputs)
	if estimatedTokens > window {
		// Both numbers, because the ratio between them is the finding: the
		// window saturates at the cap, so it alone cannot tell an input that
		// slightly overruns from one embedded almost entirely from its title.
		slog.WarnContext(ctx,
			"an embed input is longer than the largest window this adapter asks for; "+
				"its vector is computed from the head of the text and retrieval will not match the rest",
			"model", embedModel, "estimated_tokens", estimatedTokens,
			"window_tokens", window, "inputs", len(req.Inputs))
	}
	payload, _, err := sendablePayload(ctx,
		ollamaEmbedWire{Model: embedModel, Input: req.Inputs, Options: &ollamaEmbedOptions{NumCtx: window}}, nil)
	if err != nil {
		return model.Embeddings{}, err
	}
	body, err := c.post(ctx, "/api/embed", payload)
	if err != nil {
		return model.Embeddings{}, err
	}
	//craft:ignore swallowed-errors best-effort close of a response body already read to completion — the decode result decides the outcome
	defer func() { _ = body.Close() }()
	var out struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := json.NewDecoder(body).Decode(&out); err != nil {
		return model.Embeddings{}, fmt.Errorf("ai: ollama: decode embeddings: %w", err)
	}
	dims := 0
	if len(out.Embeddings) > 0 {
		dims = len(out.Embeddings[0])
	}
	return model.Embeddings{Vectors: out.Embeddings, Dims: dims}, nil
}

func (c *ollamaClient) Caps() model.Capabilities {
	// EmbedDims stays 0 (unknown): the width is a property of whichever
	// model the deployment pulled, discovered from the first Embed call.
	return model.Capabilities{Streaming: true, EmbedDims: 0, LocalOnly: true}
}

// chat sends one non-streaming /api/chat call; chatStream requests the
// JSON-lines stream of the same call. Two names so a call site says
// which wire mode it gets instead of passing a bare boolean.
func (c *ollamaClient) chat(ctx context.Context, req model.Request) (io.ReadCloser, error) {
	return c.sendChat(ctx, req, false)
}

func (c *ollamaClient) chatStream(ctx context.Context, req model.Request) (io.ReadCloser, error) {
	return c.sendChat(ctx, req, true)
}

func (c *ollamaClient) sendChat(ctx context.Context, req model.Request, stream bool) (io.ReadCloser, error) {
	// Local vision is out of scope here — reject any attachment rather than
	// silently drop it (spec §3.8, the map-or-reject invariant).
	if err := attachmentUnsupported("ollama", req.Attachments, rejectAllAttachments); err != nil {
		return nil, err
	}
	wire := ollamaWire{Model: req.Model, Stream: stream}
	if wire.Model == "" {
		wire.Model = c.defaultModel
	}
	if len(req.ResponseSchema) > 0 {
		wire.Format = req.ResponseSchema
	}
	// Ollama has no top-level system field; the system prompt travels as
	// the leading message.
	wire.Messages = wireMessages(req.System, req.Messages)
	for _, tool := range req.Tools {
		var tw ollamaToolWire
		tw.Type = "function"
		tw.Function.Name = tool.Name
		tw.Function.Description = tool.Description
		tw.Function.Parameters = tool.InputSchema
		wire.Tools = append(wire.Tools, tw)
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = ollamaMaxTokensDefault
	}
	// A budget past the largest window the adapter will ask for cannot be met
	// whatever it says, and left unclamped it overflows the window arithmetic
	// into a SMALL number — a request advertising an enormous num_predict
	// against a tiny context, which Ollama then truncates at once. Clamped here
	// rather than inside the window so the two fields cannot disagree.
	if maxTokens > ollamaMaxContext {
		maxTokens = ollamaMaxContext
	}
	// Sized last: the window has to account for the messages, tools and schema
	// just assembled, so this cannot move above them.
	wire.Options = &ollamaOptions{NumPredict: maxTokens, NumCtx: wire.contextWindow(maxTokens)}
	payload, _, err := sendablePayload(ctx, wire, req.SecretStripper)
	if err != nil {
		return nil, err
	}
	return c.post(ctx, "/api/chat", payload)
}

func (c *ollamaClient) post(ctx context.Context, path string, payload []byte) (io.ReadCloser, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("ai: ollama: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ai: ollama: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		//craft:ignore swallowed-errors best-effort close on the error path — the API status error is the answer
		defer func() { _ = resp.Body.Close() }()
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if readErr != nil {
			return nil, fmt.Errorf("ai: ollama: http %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("ai: ollama: http %d: %s", resp.StatusCode, bytes.TrimSpace(raw))
	}
	return resp.Body, nil
}

// ollamaStream reads the JSON-lines chat stream.
type ollamaStream struct {
	body    io.ReadCloser
	scanner *bufio.Scanner
}

func (s *ollamaStream) Next(ctx context.Context) (string, bool, error) {
	for s.scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return "", false, err
		}
		var ev ollamaChatEvent
		if err := json.Unmarshal(s.scanner.Bytes(), &ev); err != nil {
			return "", false, fmt.Errorf("ai: ollama: stream event: %w", err)
		}
		if ev.Done {
			return "", false, nil
		}
		if ev.Message.Content != "" {
			return ev.Message.Content, true, nil
		}
	}
	if err := s.scanner.Err(); err != nil {
		return "", false, fmt.Errorf("ai: ollama: stream: %w", err)
	}
	return "", false, nil
}

func (s *ollamaStream) Close() error { return s.body.Close() }
