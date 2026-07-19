// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package model defines the provider-agnostic LLM client seam
// (interfaces.md §4, 03b Layer 3). Model choice is config, not
// architecture: one implementation per provider (Anthropic / OpenAI /
// local vLLM/Ollama), never on the synchronous hot path, with a
// secret-stripper hook on every outbound payload and a local-only
// capability for the sovereign zero-egress profile (P7).
package model

import (
	"context"
	"encoding/json"
	"errors"
)

// ErrEmbeddingsUnsupported reports a chat provider with no embedding
// lane (embeddings are a separate lane, ai-operational-spec §1.1); the
// routing layer binds Embed to a dedicated embedder instead. Port-level
// so consumers in other modules can errors.Is against it without
// importing a provider package.
var ErrEmbeddingsUnsupported = errors.New("model: provider has no embedding lane")

// ErrAttachmentUnsupported reports an adapter that cannot carry a given
// attachment MIME on its wire (a model capability limit, parallel to
// ErrEmbeddingsUnsupported — NOT an apperrors domain sentinel). Callers route
// or surface honestly rather than silently dropping the attachment.
var ErrAttachmentUnsupported = errors.New("model: provider cannot carry this attachment type")

// Attachment is one cross-provider input part. Bytes XOR URI: Bytes for inline
// content, URI for a provider file handle / URL. Name is optional provenance.
type Attachment struct {
	MIME  string
	Bytes []byte
	URI   string
	Name  string
}

// Client is the swappable model interface; selection is config.
type Client interface {
	// Complete is a single-shot completion (summaries, draft replies,
	// NL→query-plan compilation).
	Complete(ctx context.Context, req Request) (Response, error)

	// Stream yields tokens incrementally (first-token budget 1.5s).
	Stream(ctx context.Context, req Request) (TokenStream, error)

	// Embed produces vectors for pgvector retrieval / the context graph.
	Embed(ctx context.Context, req EmbedRequest) (Embeddings, error)

	// Caps reports what the provider supports so callers route correctly
	// (cheap/local for capture+classify, premium when quality demands).
	Caps() Capabilities
}

type Request struct {
	Model     string // logical model id; config resolves it to a provider model
	System    string
	Messages  []Message
	Tools     []ToolDef
	MaxTokens int
	// ResponseSchema, when non-nil, is a JSON Schema the completion must
	// conform to. Providers with schema-constrained decoding enforce it at
	// GENERATION so a weak model cannot emit the wrong shape — Ollama via
	// `format`, vLLM via the OpenAI `response_format` json_schema, and
	// Anthropic via `output_config.format`. Providers without a native mode
	// (the offline fake) ignore it and the caller's parse→validate→retry
	// policy still catches malformed output. It is a shape guardrail, never a
	// substitute for that policy or the domain evidence gate. It is a
	// json.RawMessage (not []byte) so it is carried as a JSON value, never
	// base64-encoded, if a wire embeds it.
	ResponseSchema json.RawMessage
	// SecretStripper runs over the OUTBOUND payload before it leaves the
	// process. Hygiene only — credentials and secrets, not PII
	// pseudonymization (A8 revised); privacy is the location ladder. In
	// the sovereign profile egress is blocked entirely regardless.
	SecretStripper SecretStripper
	// ProviderOptions carries vendor-only knobs, namespaced by provider key
	// (e.g. {"openai":{"reasoning_effort":"low"}}). An adapter reads only its
	// own namespace and ignores the rest; an unknown namespace is a no-op. This
	// is how a native adapter gets reasoning/thinking/cache-control without
	// widening this interface per vendor.
	ProviderOptions map[string]json.RawMessage
	// Attachments are typed cross-provider input parts (image/pdf/audio). Each
	// capable adapter maps them to its wire; one that cannot carry a given MIME
	// returns ErrAttachmentUnsupported (never a silent drop).
	Attachments []Attachment
}

type Message struct {
	Role    string // "user" | "assistant"
	Content string
}

// ToolDef is a native tool-use declaration passed through to providers
// that support it.
type ToolDef struct {
	Name        string
	Description string
	InputSchema []byte // JSON Schema
}

type Response struct {
	Text        string
	InputTokens int
	// OutputTokens is the TOTAL billed output, reasoning/thinking tokens
	// INCLUDED — every adapter must normalize to this (Gemini reports them
	// separately; its adapter adds them back), so tokens_in+tokens_out is
	// true spend on every provider and the budget bands can't be leaked past
	// by thinking-heavy calls.
	OutputTokens int
	// CachedTokens / ReasoningTokens are the itemized usage a native provider
	// returns (prompt-cache reads, reasoning/thinking tokens). ReasoningTokens
	// is a breakdown WITHIN OutputTokens, never additive to it; an adapter
	// with no such figure leaves them 0.
	CachedTokens    int
	ReasoningTokens int
	// ProviderMetadata carries vendor-only outputs namespaced by provider key
	// (e.g. {"openai":{"response_id":"…"}} for session logging).
	ProviderMetadata map[string]json.RawMessage
	// ServedModel is the provider-reported identity of the model that actually
	// answered — read off the wire response, never fabricated by an adapter. It
	// is empty when the provider reports none (the routing layer then falls
	// back to the configured tier binding, which may differ from what actually
	// served if the vendor silently substitutes a model).
	ServedModel string
}

// TokenStream delivers incremental completion tokens; Close releases the
// underlying connection.
type TokenStream interface {
	// Next returns the next chunk; ok is false when the stream is done.
	Next(ctx context.Context) (chunk string, ok bool, err error)
	Close() error
}

type EmbedRequest struct {
	Model  string
	Inputs []string
	// Dimensions, when > 0, asks the embedder to emit vectors of exactly this
	// width — the retrieval store's fixed column width. Cloud embedders whose
	// native width differs (OpenAI text-embedding-3, Gemini gemini-embedding-001)
	// honor it via their truncation parameter; an embedder already at the width
	// (a local bge-m3, the fake) ignores it. 0 means "provider default".
	Dimensions int
}

type Embeddings struct {
	Vectors [][]float32
	Dims    int
}

// SecretStripper removes credentials/secrets (API keys, tokens,
// passwords) from a model-bound payload. Conformance-tested: secrets
// never appear in an outbound payload.
type SecretStripper interface {
	Strip(ctx context.Context, payload []byte) (stripped []byte, report StripReport, err error)
}

// StripReport says what was removed, for the audit trail.
type StripReport struct {
	Findings int
	Kinds    []string
}

type Capabilities struct {
	Streaming bool
	EmbedDims int
	// LocalOnly is true for local inference — the P7 sovereignty and
	// zero-egress path.
	LocalOnly bool
}
