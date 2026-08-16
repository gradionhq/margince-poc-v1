// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"context"
	"fmt"
	"hash/fnv"
	"math"
	"sync"

	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// FakeClient is the offline model.Client every test drives (B-EP06.2):
// fully deterministic, zero egress. Completions come from a scripted
// queue (or a stable hash-derived fallback), embeddings are seeded from
// the input text so the same text always maps to the same vector, and
// every outbound payload is recorded post-stripping so tests can assert
// what would have left the process.
type FakeClient struct {
	mu        sync.Mutex
	scripted  []string
	steps     []FakeStep
	calls     []FakeCall
	embedDims int
	carriage  []string
}

// FakeStep is one scripted Complete outcome beyond what Script's bare
// text covers: Err (when set) fails the call instead of answering it —
// the offline equivalent of a real provider's transient error, for a
// test that must force one ladder rung to fail so the router falls
// through to the next bound tier — and ServedModel (when set) overrides
// Complete's default "fake" identity literal, for a test that must prove
// two different rungs answered as two different served models.
type FakeStep struct {
	Text        string
	ServedModel string
	Err         error
}

// FakeCall is one recorded model invocation: the exact bytes that would
// have gone on the wire (after the SecretStripper ran) and what the
// stripper removed.
type FakeCall struct {
	Op      string // "complete" | "stream" | "embed"
	Model   string
	Payload []byte
	Report  model.StripReport
}

// fakeEmbedDims matches the embedding column width the retrieval
// substrate provisions, so fake vectors round-trip through pgvector.
const fakeEmbedDims = 1024

func NewFakeClient() *FakeClient {
	return &FakeClient{embedDims: fakeEmbedDims, carriage: carriesImagesAndPDF}
}

// CarryingNothing makes the fake declare a text-only wire, so a test can drive
// what a caller does when its binding cannot carry a document at all — the one
// thing the default carriage cannot express.
func (f *FakeClient) CarryingNothing() *FakeClient {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.carriage = carriesNothing
	return f
}

// Script queues completion texts returned in order by Complete/Stream.
// When the queue runs dry the fake falls back to a deterministic
// payload-hash response, so unscripted tests still get stable output.
func (f *FakeClient) Script(texts ...string) *FakeClient {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scripted = append(f.scripted, texts...)
	return f
}

// ScriptSteps queues full Complete outcomes, consumed in order ahead of
// any bare text queued via Script — for a test that needs more than a
// scripted answer (a scripted failure, or an explicit served-model
// identity) on a specific call. Applies to Complete only: Stream still
// consumes Script's plain-text queue.
func (f *FakeClient) ScriptSteps(steps ...FakeStep) *FakeClient {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.steps = append(f.steps, steps...)
	return f
}

// Calls returns a copy of every recorded invocation.
func (f *FakeClient) Calls() []FakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]FakeCall(nil), f.calls...)
}

func (f *FakeClient) Complete(ctx context.Context, req model.Request) (model.Response, error) {
	// The fake obeys map-or-reject like every real adapter: a media type it does
	// not declare gets the sentinel, never a silent drop. Without this the fake
	// is the one client whose offline behaviour differs from production exactly
	// where a test would fail to notice.
	if err := attachmentUnsupported("fake", req.Attachments, f.Caps().AttachmentMIMEs); err != nil {
		return model.Response{}, err
	}
	payload, report, err := sendablePayload(ctx, fakeWire(req), req.SecretStripper)
	if err != nil {
		return model.Response{}, err
	}
	step := f.nextStep(payload)
	f.record(FakeCall{Op: "complete", Model: req.Model, Payload: payload, Report: report})
	if step.Err != nil {
		return model.Response{}, step.Err
	}
	// ServedModel defaults to the literal "fake": the fake client IS the
	// wire, so this is exactly as honest as a real adapter's
	// provider-reported field. A scripted FakeStep.ServedModel overrides
	// it — only a test proving something served-identity-sensitive needs
	// to set this explicitly.
	servedModel := step.ServedModel
	if servedModel == "" {
		servedModel = "fake"
	}
	return model.Response{
		Text: step.Text,
		// Rough 4-bytes-per-token estimate — enough for metering and
		// budget tests to see plausible, deterministic numbers.
		InputTokens:  len(payload) / 4,
		OutputTokens: len(step.Text) / 4,
		ServedModel:  servedModel,
	}, nil
}

func (f *FakeClient) Stream(ctx context.Context, req model.Request) (model.TokenStream, error) {
	payload, report, err := sendablePayload(ctx, fakeWire(req), req.SecretStripper)
	if err != nil {
		return nil, err
	}
	text := f.nextText(payload)
	f.record(FakeCall{Op: "stream", Model: req.Model, Payload: payload, Report: report})
	return &sliceStream{chunks: chunkText(text, 16)}, nil
}

func (f *FakeClient) Embed(ctx context.Context, req model.EmbedRequest) (model.Embeddings, error) {
	payload, _, err := sendablePayload(ctx, req.Inputs, nil)
	if err != nil {
		return model.Embeddings{}, err
	}
	f.record(FakeCall{Op: "embed", Model: req.Model, Payload: payload})
	// Dimensions, when set, is the caller's requested vector width (e.g.
	// the Router filling it from the configured embeddings binding) — the
	// fake must honor it exactly, the same way a real adapter forwards it
	// on the wire, rather than always answering at its own default width.
	dims := req.Dimensions
	if dims == 0 {
		dims = f.embedDims
	}
	vectors := make([][]float32, len(req.Inputs))
	for i, input := range req.Inputs {
		vectors[i] = deterministicVector(input, dims)
	}
	return model.Embeddings{Vectors: vectors, Dims: dims}, nil
}

// Caps declares the fake carries documents by default, because the fake stands
// in for a real model on every offline lane: a fake that quietly declared less
// would send every offline test down the cannot-carry fallback and prove
// nothing about the path that actually ships. CarryingNothing is how a test
// asks for the other side deliberately.
func (f *FakeClient) Caps() model.Capabilities {
	f.mu.Lock()
	defer f.mu.Unlock()
	return model.Capabilities{
		Streaming:       true,
		EmbedDims:       f.embedDims,
		LocalOnly:       true,
		AttachmentMIMEs: f.carriage,
	}
}

func (f *FakeClient) record(call FakeCall) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
}

func (f *FakeClient) nextText(payload []byte) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.scripted) > 0 {
		text := f.scripted[0]
		f.scripted = f.scripted[1:]
		return text
	}
	h := fnv.New64a()
	_, _ = h.Write(payload) // fnv never errors
	return fmt.Sprintf("fake-completion:%016x", h.Sum64())
}

// nextStep pops the next queued FakeStep for Complete, falling back to a
// bare-text step from Script's queue (or the deterministic payload-hash
// fallback, once both queues run dry) unlocked by the same rules
// nextText already applies to Stream.
func (f *FakeClient) nextStep(payload []byte) FakeStep {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.steps) > 0 {
		step := f.steps[0]
		f.steps = f.steps[1:]
		return step
	}
	if len(f.scripted) > 0 {
		text := f.scripted[0]
		f.scripted = f.scripted[1:]
		return FakeStep{Text: text}
	}
	h := fnv.New64a()
	_, _ = h.Write(payload) // fnv never errors
	return FakeStep{Text: fmt.Sprintf("fake-completion:%016x", h.Sum64())}
}

// fakeWire mirrors the shape a real adapter marshals, so stripper
// conformance tested against the fake carries over to the cloud path.
func fakeWire(req model.Request) map[string]any {
	return map[string]any{
		"model":       req.Model,
		"system":      req.System,
		"messages":    fakeMessageWire(req.Messages),
		"tools":       req.Tools,
		"attachments": fakeAttachmentWire(req.Attachments),
	}
}

// fakeMessage renders one turn as the lowercase {role, content} pair every real
// wire is some elaboration of. It is deliberately none of them: each real
// adapter owns its own message type because each spells a carried image
// differently, and the fake asserts nothing about those spellings — what it
// mirrors is that the CONTENT of a turn reaches the payload the stripper runs
// over.
//
// A struct rather than a map, because field order is load-bearing here: the
// fake's fallback completion is a hash OF THESE BYTES, and Go marshals map keys
// in sorted order — so a map would silently re-order `role` after `content` and
// change every deterministic answer this client gives.
type fakeMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func fakeMessageWire(msgs []model.Message) []fakeMessage {
	out := make([]fakeMessage, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, fakeMessage{Role: m.Role, Content: m.Content})
	}
	return out
}

// fakeAttachmentWire renders each carried attachment as its media type, its
// name and its SIZE rather than its bytes. The recorded payload is what tests
// assert on and what the secret stripper runs over, and a megabyte of base64 in
// every one would make both useless — while omitting attachments entirely,
// which this wire did before documents reached it, made the fake the one
// adapter that silently dropped an input every real adapter must map or refuse.
func fakeAttachmentWire(atts []model.Attachment) []map[string]any {
	if len(atts) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(atts))
	for _, a := range atts {
		out = append(out, map[string]any{"mime": a.MIME, "name": a.Name, "bytes": len(a.Bytes), "uri": a.URI})
	}
	return out
}

// deterministicVector expands an FNV seed of the text through an
// xorshift generator into a unit-length vector: same text, same vector,
// across processes and runs — the property the embedding cache and the
// pgvector integration tests rely on.
func deterministicVector(text string, dims int) []float32 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(text)) // fnv never errors
	state := h.Sum64() | 1       // xorshift must not start at zero
	vec := make([]float32, dims)
	var norm float64
	for i := range vec {
		state ^= state << 13
		state ^= state >> 7
		state ^= state << 17
		// Top 53 bits → [0,1) exactly representable, then shift to [-1,1).
		v := float64(state>>11)/float64(1<<53)*2 - 1
		vec[i] = float32(v)
		norm += v * v
	}
	scale := float32(1 / math.Sqrt(norm))
	for i := range vec {
		vec[i] *= scale
	}
	return vec
}

type sliceStream struct {
	chunks []string
}

func (s *sliceStream) Next(context.Context) (string, bool, error) {
	if len(s.chunks) == 0 {
		return "", false, nil
	}
	chunk := s.chunks[0]
	s.chunks = s.chunks[1:]
	return chunk, true, nil
}

func (s *sliceStream) Close() error { return nil }

func chunkText(text string, size int) []string {
	var chunks []string
	for len(text) > size {
		chunks = append(chunks, text[:size])
		text = text[size:]
	}
	if text != "" {
		chunks = append(chunks, text)
	}
	return chunks
}
