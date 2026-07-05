// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

func newVLLMForTest(t *testing.T, handler http.HandlerFunc) model.Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client, err := SelectBrain(ProviderConfig{Provider: "vllm", Model: "google/gemma-3-12b-it", BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestVLLMCompleteSpeaksOpenAICompatibleWire(t *testing.T) {
	var received []byte
	client := newVLLMForTest(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("wrong path %s", r.URL.Path)
		}
		received, _ = io.ReadAll(r.Body)
		if err := json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": "local hello"}}},
			"usage":   map[string]int{"prompt_tokens": 7, "completion_tokens": 2},
		}); err != nil {
			t.Errorf("encoding fixture response: %v", err)
		}
	})
	resp, err := client.Complete(context.Background(), model.Request{
		System:         "be terse",
		Messages:       []model.Message{{Role: "user", Content: "with password=verysecretpw inside"}},
		MaxTokens:      64,
		SecretStripper: NewSecretStripper(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "local hello" || resp.InputTokens != 7 || resp.OutputTokens != 2 {
		t.Fatalf("response mapping wrong: %+v", resp)
	}
	var wire struct {
		Model     string          `json:"model"`
		Messages  []model.Message `json:"messages"`
		MaxTokens int             `json:"max_tokens"`
	}
	if err := json.Unmarshal(received, &wire); err != nil {
		t.Fatalf("wire not JSON: %v", err)
	}
	if wire.Model != "google/gemma-3-12b-it" || len(wire.Messages) != 2 || wire.Messages[0].Role != "system" {
		t.Fatalf("system message not first: %+v", wire)
	}
	if wire.MaxTokens != 64 {
		t.Fatalf("max_tokens not carried: %+v", wire)
	}
	// The stripper runs on the LOCAL path too (B-EP06.5: cloud or local).
	if strings.Contains(string(received), "verysecretpw") {
		t.Fatalf("secret reached the local wire: %s", received)
	}
}

func TestVLLMEmbedReturnsVectors(t *testing.T) {
	client := newVLLMForTest(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			t.Fatalf("wrong path %s", r.URL.Path)
		}
		if err := json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"embedding": []float32{0.1, 0.2, 0.3}, "index": 0},
				{"embedding": []float32{0.4, 0.5, 0.6}, "index": 1},
			},
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

func TestVLLMStreamReadsSSEDeltas(t *testing.T) {
	client := newVLLMForTest(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `data: {"choices":[{"delta":{"content":"lo"}}]}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"choices":[{"delta":{"content":"cal"}}]}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
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

func TestVLLMReportsLocalOnly(t *testing.T) {
	client, err := SelectBrain(ProviderConfig{Provider: "vllm"})
	if err != nil {
		t.Fatal(err)
	}
	if !client.Caps().LocalOnly {
		t.Fatal("vllm must report LocalOnly=true — it is a sovereign-profile provider")
	}
}

// The unbound local path must land on a Gemma-class (non-Chinese)
// default per ADR-0012/A23; an operator choosing Mistral or anything
// else does so explicitly in ai-routing.yaml.
func TestLocalProvidersDefaultToGemmaClassModels(t *testing.T) {
	for provider, wantModel := range map[string]string{
		"ollama": defaultOllamaModel,
		"vllm":   defaultVLLMModel,
	} {
		if !strings.Contains(wantModel, "gemma") {
			t.Fatalf("%s default %q is not Gemma-class (A23)", provider, wantModel)
		}
		client, err := SelectBrain(ProviderConfig{Provider: provider})
		if err != nil {
			t.Fatal(err)
		}
		got := ""
		switch c := client.(type) {
		case *ollamaClient:
			got = c.defaultModel
		case *vllmClient:
			got = c.defaultModel
		default:
			t.Fatalf("%s built an unexpected client type %T", provider, client)
		}
		if got != wantModel {
			t.Fatalf("%s unbound model default = %q, want %q (A23)", provider, got, wantModel)
		}
	}
}

func TestSovereignProfileAdmitsVLLM(t *testing.T) {
	cfg := []byte(`
profile: sovereign
tiers:
  local_small: {provider: vllm}
  local_large: {provider: vllm, model: google/gemma-3-27b-it}
embeddings: {provider: vllm}
`)
	if _, err := ParseRouting(cfg); err != nil {
		t.Fatalf("vllm must be admissible under the sovereign profile: %v", err)
	}
}
