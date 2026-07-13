// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
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
