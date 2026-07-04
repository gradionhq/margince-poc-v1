// Package model defines the provider-agnostic LLM client seam
// (interfaces.md §4, 03b Layer 3). Model choice is config, not
// architecture: one implementation per provider (Anthropic / OpenAI /
// local vLLM/Ollama), never on the synchronous hot path, with a
// secret-stripper hook on every outbound payload and a local-only
// capability for the sovereign zero-egress profile (P7).
package model

import (
	"context"
	"errors"
)

// ErrEmbeddingsUnsupported reports a chat provider with no embedding
// lane (embeddings are a separate lane, ai-operational-spec §1.1); the
// routing layer binds Embed to a dedicated embedder instead. Port-level
// so consumers in other modules can errors.Is against it without
// importing a provider package.
var ErrEmbeddingsUnsupported = errors.New("model: provider has no embedding lane")

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
	// SecretStripper runs over the OUTBOUND payload before it leaves the
	// process. Hygiene only — credentials and secrets, not PII
	// pseudonymization (A8 revised); privacy is the location ladder. In
	// the sovereign profile egress is blocked entirely regardless.
	SecretStripper SecretStripper
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
	Text         string
	InputTokens  int
	OutputTokens int
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
