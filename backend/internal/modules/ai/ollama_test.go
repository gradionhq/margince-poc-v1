// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

func newOllamaForTest(t *testing.T, handler http.HandlerFunc) model.Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client, err := SelectBrain(ProviderConfig{Provider: "ollama", Model: "gemma3", BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestOllamaCompleteCarriesSystemAsLeadingMessage(t *testing.T) {
	var received []byte
	client := newOllamaForTest(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Fatalf("wrong path %s", r.URL.Path)
		}
		received = readBody(t, r.Body)
		if err := json.NewEncoder(w).Encode(map[string]any{
			"model":   "gemma3:served",
			"message": map[string]string{"content": "local hello"},
			"done":    true, "prompt_eval_count": 7, "eval_count": 2,
		}); err != nil {
			t.Errorf("encoding fixture response: %v", err)
		}
	})
	resp, err := client.Complete(context.Background(), model.Request{
		System:         "be terse",
		Messages:       []model.Message{{Role: "user", Content: "with password=verysecretpw inside"}},
		SecretStripper: NewSecretStripper(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "local hello" || resp.InputTokens != 7 || resp.OutputTokens != 2 {
		t.Fatalf("response mapping wrong: %+v", resp)
	}
	if resp.ServedModel != "gemma3:served" {
		t.Fatalf("ServedModel not decoded from the response's own model field: %q", resp.ServedModel)
	}
	var wire struct {
		Model    string          `json:"model"`
		Messages []model.Message `json:"messages"`
	}
	if err := json.Unmarshal(received, &wire); err != nil {
		t.Fatalf("wire not JSON: %v", err)
	}
	if wire.Model != "gemma3" || len(wire.Messages) != 2 || wire.Messages[0].Role != "system" {
		t.Fatalf("system message not first: %+v", wire)
	}
	// The stripper runs on the LOCAL path too (B-EP06.5: cloud or local).
	if strings.Contains(string(received), "verysecretpw") {
		t.Fatalf("secret reached the local wire: %s", received)
	}
}

func TestOllamaCarriesResponseSchemaAsFormatWhenSetAndOmitsItOtherwise(t *testing.T) {
	schema := []byte(`{"type":"object","properties":{"ok":{"type":"boolean"}}}`)
	capture := func(t *testing.T, req model.Request) map[string]json.RawMessage {
		t.Helper()
		var received []byte
		client := newOllamaForTest(t, func(w http.ResponseWriter, r *http.Request) {
			received = readBody(t, r.Body)
			if err := json.NewEncoder(w).Encode(map[string]any{
				"message": map[string]string{"content": "{}"}, "done": true,
			}); err != nil {
				t.Errorf("encoding fixture response: %v", err)
			}
		})
		if _, err := client.Complete(context.Background(), req); err != nil {
			t.Fatal(err)
		}
		var wire map[string]json.RawMessage
		if err := json.Unmarshal(received, &wire); err != nil {
			t.Fatalf("wire not JSON: %v", err)
		}
		return wire
	}

	// With a schema, the wire carries it VERBATIM as `format` so Ollama
	// constrains decoding to it.
	withSchema := capture(t, model.Request{
		Messages:       []model.Message{{Role: "user", Content: "hi"}},
		ResponseSchema: schema,
	})
	got, ok := withSchema["format"]
	if !ok {
		t.Fatalf("format absent when ResponseSchema set: %v", withSchema)
	}
	if !bytes.Equal(bytes.TrimSpace(got), bytes.TrimSpace(schema)) {
		t.Fatalf("format not the schema verbatim: %s", got)
	}

	// Without a schema, `format` MUST be omitted — an empty/`null` format
	// would make Ollama reject or misparse every ordinary call.
	noSchema := capture(t, model.Request{Messages: []model.Message{{Role: "user", Content: "hi"}}})
	if _, present := noSchema["format"]; present {
		t.Fatalf("format present when no ResponseSchema: %v", noSchema)
	}
}

func TestOllamaEmbedReturnsVectors(t *testing.T) {
	client := newOllamaForTest(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			t.Fatalf("wrong path %s", r.URL.Path)
		}
		if err := json.NewEncoder(w).Encode(map[string]any{
			"embeddings": [][]float32{{0.1, 0.2, 0.3}, {0.4, 0.5, 0.6}},
		}); err != nil {
			t.Errorf("encoding fixture response: %v", err)
		}
	})
	res, err := client.Embed(context.Background(), model.EmbedRequest{Inputs: []string{"a", "b"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Dims != 3 || len(res.Vectors) != 2 {
		t.Fatalf("unexpected shape: %+v", res)
	}
}

func TestOllamaStreamReadsJSONLines(t *testing.T) {
	client := newOllamaForTest(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"message":{"content":"lo"},"done":false}`+"\n")
		_, _ = io.WriteString(w, `{"message":{"content":"cal"},"done":false}`+"\n")
		_, _ = io.WriteString(w, `{"message":{"content":""},"done":true,"eval_count":2}`+"\n")
	})
	stream, err := client.Stream(context.Background(), model.Request{Messages: []model.Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := stream.Close(); err != nil {
			t.Errorf("closing stream: %v", err)
		}
	}()
	var got strings.Builder
	for {
		chunk, ok, err := stream.Next(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		got.WriteString(chunk)
	}
	if got.String() != "local" {
		t.Fatalf("stream mismatch: %q", got.String())
	}
}

// num_ctx bounds prompt AND completion together; num_predict bounds only the
// completion. Left unsaid, Ollama uses a 4096 default, so a long page and a
// large output budget cannot both fit — and the model stops mid-generation
// rather than refusing. On a reasoning model the cut lands inside the thinking,
// so the reply arrives well-formed with an EMPTY content field.
//
// Ground truth here is the wire Ollama actually received, never the estimator's
// own arithmetic: an assertion that recomputed the estimate would still hold if
// the estimate itself regressed.
func TestOllamaSizesContextWindowToPromptPlusOutputBudget(t *testing.T) {
	sent := func(t *testing.T, req model.Request) (map[string]json.RawMessage, ollamaOptions, int) {
		t.Helper()
		var received []byte
		client := newOllamaForTest(t, func(w http.ResponseWriter, r *http.Request) {
			received = readBody(t, r.Body)
			if err := json.NewEncoder(w).Encode(map[string]any{
				"message": map[string]string{"content": "{}"}, "done": true,
			}); err != nil {
				t.Errorf("encoding fixture response: %v", err)
			}
		})
		if _, err := client.Complete(context.Background(), req); err != nil {
			t.Fatal(err)
		}
		var raw struct {
			Options map[string]json.RawMessage `json:"options"`
		}
		if err := json.Unmarshal(received, &raw); err != nil {
			t.Fatalf("wire not JSON: %v", err)
		}
		var opts ollamaOptions
		encoded, err := json.Marshal(raw.Options)
		if err != nil {
			t.Fatalf("re-encode options: %v", err)
		}
		if err := json.Unmarshal(encoded, &opts); err != nil {
			t.Fatalf("options not decodable: %v", err)
		}
		return raw.Options, opts, len(received)
	}

	page := strings.Repeat("Gradion Pte. Ltd. 77 High Street, Singapore. ", 400)
	schema := []byte(`{"type":"object","properties":{"facts":{"type":"array"}}}`)

	for _, tc := range []struct {
		name string
		req  model.Request
	}{
		{"long page with a budget", model.Request{
			System:         "You extract company facts from ONE page.",
			Messages:       []model.Message{{Role: "user", Content: page}},
			MaxTokens:      8192,
			ResponseSchema: schema,
		}},
		{"long page with no budget named", model.Request{
			Messages: []model.Message{{Role: "user", Content: page}},
		}},
		{"long page with tools", model.Request{
			Messages: []model.Message{{Role: "user", Content: page}},
			Tools: []model.ToolDef{{
				Name: "lookup", Description: strings.Repeat("finds a record. ", 200),
				InputSchema: schema,
			}},
			MaxTokens: 2048,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, opts, wireBytes := sent(t, tc.req)
			// The window must hold everything actually sent plus everything the
			// model was authorised to generate. wireBytes is the whole payload,
			// so it over-counts the JSON scaffolding — which only makes this a
			// stricter floor than the prompt alone.
			if floor := wireBytes/4 + opts.NumPredict; opts.NumCtx < floor {
				t.Fatalf(
					"num_ctx %d cannot hold the %d-byte payload (~%d tok) plus num_predict %d — the model stops mid-generation",
					opts.NumCtx, wireBytes, wireBytes/4, opts.NumPredict,
				)
			}
			if opts.NumPredict <= 0 {
				t.Fatalf("num_predict %d — a request with no stated budget must still get one", opts.NumPredict)
			}
			if _, present := raw["num_ctx"]; !present {
				t.Fatalf("num_ctx absent from the wire: %v", raw)
			}
			if opts.NumCtx%ollamaContextBucket != 0 {
				t.Fatalf("num_ctx %d is not a bucket multiple — a per-request value reloads the runner", opts.NumCtx)
			}
			if opts.NumCtx < ollamaContextFloor || opts.NumCtx > ollamaMaxContext {
				t.Fatalf("num_ctx %d outside [%d,%d]", opts.NumCtx, ollamaContextFloor, ollamaMaxContext)
			}
		})
	}

	// A short request must not come out worse than saying nothing at all.
	_, small, _ := sent(t, model.Request{Messages: []model.Message{{Role: "user", Content: "hi"}}})
	if small.NumCtx != ollamaContextFloor {
		t.Fatalf("num_ctx %d for a two-byte prompt, want the %d floor", small.NumCtx, ollamaContextFloor)
	}

	// The window tracks the prompt rather than sitting at a constant: a bigger
	// page must buy a bigger window, which a flat default would not.
	_, grown, _ := sent(t, model.Request{
		Messages:  []model.Message{{Role: "user", Content: strings.Repeat(page, 4)}},
		MaxTokens: 2048,
	})
	_, base, _ := sent(t, model.Request{
		Messages:  []model.Message{{Role: "user", Content: page}},
		MaxTokens: 2048,
	})
	if grown.NumCtx <= base.NumCtx {
		t.Fatalf("num_ctx did not grow with the prompt: %d then %d", base.NumCtx, grown.NumCtx)
	}

	// An absurd budget must clamp rather than wrap. Unclamped, `prompt/4 +
	// maxTokens` overflows and the bucket arithmetic lands on a SMALL window —
	// the model would be handed a huge num_predict against a tiny context and
	// truncate immediately, which is the opposite of what the number says.
	_, absurd, _ := sent(t, model.Request{
		Messages:  []model.Message{{Role: "user", Content: "hi"}},
		MaxTokens: math.MaxInt32,
	})
	if absurd.NumCtx != ollamaMaxContext {
		t.Fatalf("num_ctx %d for an absurd budget, want the %d cap", absurd.NumCtx, ollamaMaxContext)
	}
	if absurd.NumPredict > absurd.NumCtx {
		t.Fatalf(
			"num_predict %d exceeds num_ctx %d — the budget promises more than the window can hold",
			absurd.NumPredict, absurd.NumCtx,
		)
	}

	// Prompt length is chosen by whoever published the page, so the window it
	// can ask for is capped: past the ceiling the prompt truncates, rather than
	// a remote party sizing the host's KV-cache allocation.
	_, hostile, _ := sent(t, model.Request{
		Messages:  []model.Message{{Role: "user", Content: strings.Repeat("a", 4<<20)}},
		MaxTokens: 8192,
	})
	if hostile.NumCtx != ollamaMaxContext {
		t.Fatalf("num_ctx %d for a 4MiB prompt, want the %d cap", hostile.NumCtx, ollamaMaxContext)
	}
}

// ollamaEmbedReply is what /api/embed answers, written as the shape the adapter
// decodes rather than as an untyped map: a fixture that cannot say what field it
// is filling can drift from the wire it stands for without failing anything.
type ollamaEmbedReply struct {
	Embeddings [][]float32 `json:"embeddings"`
}

// The embed lane sizes its window for the same reason the chat lane does, and
// for one it does not: a chat request past its window stops generating and says
// so, while an embedding past its window comes back the right width, computed
// from the head of the text. Nothing downstream can tell that apart from a whole
// vector, so these assertions are the only place the difference is visible.
func TestOllamaSizesTheEmbedWindowToTheLongestInput(t *testing.T) {
	sent := func(t *testing.T, inputs []string) (map[string]json.RawMessage, ollamaEmbedOptions) {
		t.Helper()
		var received []byte
		client := newOllamaForTest(t, func(w http.ResponseWriter, r *http.Request) {
			received = readBody(t, r.Body)
			vectors := make([][]float32, len(inputs))
			for i := range vectors {
				vectors[i] = []float32{0.1}
			}
			if err := json.NewEncoder(w).Encode(ollamaEmbedReply{Embeddings: vectors}); err != nil {
				t.Errorf("encoding fixture response: %v", err)
			}
		})
		if _, err := client.Embed(context.Background(), model.EmbedRequest{Inputs: inputs}); err != nil {
			t.Fatal(err)
		}
		var raw struct {
			Options map[string]json.RawMessage `json:"options"`
		}
		if err := json.Unmarshal(received, &raw); err != nil {
			t.Fatalf("wire not JSON: %v", err)
		}
		encoded, err := json.Marshal(raw.Options)
		if err != nil {
			t.Fatalf("re-encode options: %v", err)
		}
		var opts ollamaEmbedOptions
		if err := json.Unmarshal(encoded, &opts); err != nil {
			t.Fatalf("options not decodable: %v", err)
		}
		return raw.Options, opts
	}

	document := strings.Repeat("Gradion Pte. Ltd. 77 High Street, Singapore. ", 400)

	raw, opts := sent(t, []string{document})
	if _, present := raw["num_ctx"]; !present {
		t.Fatalf("num_ctx absent from the embed wire: %v — the model embeds a prefix of whatever exceeds its loaded window", raw)
	}
	if floor := len(document) / 4; opts.NumCtx < floor {
		t.Fatalf("num_ctx %d cannot hold a %d-byte document (~%d tok): the vector is computed from its head",
			opts.NumCtx, len(document), floor)
	}
	if opts.NumCtx%ollamaContextBucket != 0 {
		t.Fatalf("num_ctx %d is not a bucket multiple — a per-document value reloads the runner, so a reindex pays a model load per record", opts.NumCtx)
	}
	if opts.NumCtx < ollamaContextFloor || opts.NumCtx > ollamaMaxContext {
		t.Fatalf("num_ctx %d outside [%d,%d]", opts.NumCtx, ollamaContextFloor, ollamaMaxContext)
	}

	// A short input must not come out worse than saying nothing at all.
	if _, small := sent(t, []string{"hi"}); small.NumCtx != ollamaContextFloor {
		t.Fatalf("num_ctx %d for a two-byte input, want the %d floor", small.NumCtx, ollamaContextFloor)
	}

	// The window tracks the input rather than sitting at a constant, which is
	// what tells a sized call apart from a hard-coded one.
	_, base := sent(t, []string{document})
	_, grown := sent(t, []string{strings.Repeat(document, 4)})
	if grown.NumCtx <= base.NumCtx {
		t.Fatalf("num_ctx did not grow with the input: %d then %d", base.NumCtx, grown.NumCtx)
	}

	// Document length is chosen by whoever wrote the record, so the window it can
	// ask for is capped: past the ceiling the input truncates, rather than a
	// stored note sizing the host's KV-cache allocation.
	if _, hostile := sent(t, []string{strings.Repeat("a", 4<<20)}); hostile.NumCtx != ollamaMaxContext {
		t.Fatalf("num_ctx %d for a 4MiB input, want the %d cap", hostile.NumCtx, ollamaMaxContext)
	}

	// A batch is sized by its LONGEST member, not by the sum of its members:
	// num_ctx is the per-sequence window and /api/embed embeds each input on its
	// own, so summing would ask the runner for an allocation the work never
	// needs — reaching the ceiling on a batch of ordinary documents.
	_, batched := sent(t, []string{document, document, document, document})
	if batched.NumCtx != base.NumCtx {
		t.Fatalf("num_ctx %d for four copies of a document that alone asks for %d — the batch is being summed rather than measured",
			batched.NumCtx, base.NumCtx)
	}
}

// The warning is the whole of what this adapter can do about an input past the
// ceiling: it still embeds the prefix, because refusing would strand an
// oversized record in the index consumer forever. So the fact has to be in a log
// exactly when it is true — a warning that never fires and one that always fires
// are equally useless to whoever is chasing thin retrieval results.
func TestOllamaSaysWhenAnEmbedInputOverrunsTheWindowAndIsSilentWhenItDoesNot(t *testing.T) {
	embed := func(t *testing.T, input string) string {
		t.Helper()
		var logged bytes.Buffer
		restore := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelWarn})))
		t.Cleanup(func() { slog.SetDefault(restore) })

		client := newOllamaForTest(t, func(w http.ResponseWriter, _ *http.Request) {
			if err := json.NewEncoder(w).Encode(ollamaEmbedReply{Embeddings: [][]float32{{0.1}}}); err != nil {
				t.Errorf("encoding fixture response: %v", err)
			}
		})
		if _, err := client.Embed(context.Background(), model.EmbedRequest{Inputs: []string{input}}); err != nil {
			t.Fatal(err)
		}
		return logged.String()
	}

	// 4 MiB is ~1M tokens against a 32k ceiling: the vector covers about 3% of it.
	line := embed(t, strings.Repeat("a", 4<<20))
	if !strings.Contains(line, "computed from the head of the text") {
		t.Errorf("a truncated embedding was logged as %q, want the truncation named", line)
	}
	// The window saturates at the cap, so it alone cannot tell an input that
	// slightly overruns from one embedded almost entirely from its opening
	// sentence. Without the estimate beside it the operator reading this line
	// knows that something was dropped and nothing about how much.
	for _, want := range []string{"model=gemma3", "estimated_tokens=1048576", "window_tokens=32768"} {
		if !strings.Contains(line, want) {
			t.Errorf("the truncation warning does not carry %s: %q", want, line)
		}
	}
	// Exactly at the ceiling the window still holds the input, so there is
	// nothing to report and a line here would train the reader to ignore it.
	if line := embed(t, strings.Repeat("a", ollamaMaxContext*4)); strings.Contains(line, "computed from the head of the text") {
		t.Errorf("an input the window holds was reported as truncated: %q", line)
	}
}
