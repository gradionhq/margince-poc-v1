// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

func newAnthropicForTest(t *testing.T, handler http.HandlerFunc) model.Client {
	t.Helper()
	t.Setenv("ANTHROPIC_API_KEY", "test-key") // the BYOK key rides the environment
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client, err := SelectBrain(ProviderConfig{Provider: "anthropic", Model: "claude-test", BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestAnthropicCarriesResponseSchemaAsOutputConfigFormatAndOmitsItOtherwise(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"ok":{"type":"boolean"}},"required":["ok"]}`)
	capture := func(t *testing.T, req model.Request) map[string]json.RawMessage {
		t.Helper()
		var received []byte
		client := newAnthropicForTest(t, func(w http.ResponseWriter, r *http.Request) {
			received = readBody(t, r.Body)
			if err := json.NewEncoder(w).Encode(map[string]any{
				"content": []map[string]any{{"type": "text", "text": "{}"}},
				"usage":   map[string]int{"input_tokens": 1, "output_tokens": 1},
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

	// With a schema, output_config.format carries json_schema + the schema verbatim.
	withSchema := capture(t, model.Request{
		Messages:       []model.Message{{Role: "user", Content: "hi"}},
		ResponseSchema: schema,
	})
	oc, ok := withSchema["output_config"]
	if !ok {
		t.Fatalf("output_config absent when ResponseSchema set: %v", withSchema)
	}
	var got struct {
		Format struct {
			Type   string          `json:"type"`
			Schema json.RawMessage `json:"schema"`
		} `json:"format"`
	}
	if err := json.Unmarshal(oc, &got); err != nil {
		t.Fatal(err)
	}
	if got.Format.Type != "json_schema" {
		t.Fatalf("format type wrong: %+v", got)
	}
	if !bytes.Equal(bytes.TrimSpace(got.Format.Schema), bytes.TrimSpace(schema)) {
		t.Fatalf("schema not verbatim: %s", got.Format.Schema)
	}

	// Without a schema, output_config MUST be omitted.
	noSchema := capture(t, model.Request{Messages: []model.Message{{Role: "user", Content: "hi"}}})
	if _, present := noSchema["output_config"]; present {
		t.Fatalf("output_config present when no ResponseSchema: %v", noSchema)
	}
}

func TestAnthropicDropsSchemaAndRetriesWhenOutputConfigRejected(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"ok":{"type":"boolean"}},"required":["ok"]}`)
	var calls int
	var sawSchemaThenPlain []bool
	client := newAnthropicForTest(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		var wire map[string]json.RawMessage
		if err := json.Unmarshal(readBody(t, r.Body), &wire); err != nil {
			t.Errorf("wire not JSON: %v", err)
		}
		_, hasOutputConfig := wire["output_config"]
		sawSchemaThenPlain = append(sawSchemaThenPlain, hasOutputConfig)
		if hasOutputConfig {
			// Simulate a model/endpoint that does not support structured output.
			w.WriteHeader(http.StatusBadRequest)
			if err := json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]string{"type": "invalid_request_error", "message": "output_config is not supported for this model"},
			}); err != nil {
				t.Errorf("encoding error fixture: %v", err)
			}
			return
		}
		if err := json.NewEncoder(w).Encode(map[string]any{
			"content": []map[string]any{{"type": "text", "text": "grounded"}},
			"usage":   map[string]int{"input_tokens": 1, "output_tokens": 1},
		}); err != nil {
			t.Errorf("encoding response fixture: %v", err)
		}
	})

	resp, err := client.Complete(context.Background(), model.Request{
		Messages:       []model.Message{{Role: "user", Content: "hi"}},
		ResponseSchema: schema,
	})
	if err != nil {
		t.Fatalf("schema-rejection fallback should have succeeded, got: %v", err)
	}
	if resp.Text != "grounded" {
		t.Fatalf("unexpected text after fallback: %q", resp.Text)
	}
	// First attempt carries the schema (rejected), second drops it (accepted).
	if calls != 2 || len(sawSchemaThenPlain) != 2 || !sawSchemaThenPlain[0] || sawSchemaThenPlain[1] {
		t.Fatalf("expected schema attempt then unconstrained retry, got %d calls: %v", calls, sawSchemaThenPlain)
	}
}

func TestAnthropicDoesNotRetryA400WhenNoSchemaWasSent(t *testing.T) {
	var calls int
	client := newAnthropicForTest(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
		if err := json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]string{"type": "invalid_request_error", "message": "bad request"},
		}); err != nil {
			t.Errorf("encoding error fixture: %v", err)
		}
	})
	if _, err := client.Complete(context.Background(), model.Request{
		Messages: []model.Message{{Role: "user", Content: "hi"}},
	}); err == nil {
		t.Fatal("expected the 400 to surface")
	}
	if calls != 1 {
		t.Fatalf("a 400 with no schema attached must not be retried, got %d calls", calls)
	}
}

func TestAnthropicCompleteSendsStrippedPayload(t *testing.T) {
	var received []byte
	var gotKey, gotVersion string
	client := newAnthropicForTest(t, func(w http.ResponseWriter, r *http.Request) {
		received = readBody(t, r.Body)
		gotKey = r.Header.Get("X-Api-Key")
		gotVersion = r.Header.Get("Anthropic-Version")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"model":   "claude-served-x",
			"content": []map[string]any{{"type": "text", "text": "hello"}},
			"usage":   map[string]int{"input_tokens": 12, "output_tokens": 3},
		}); err != nil {
			t.Errorf("encoding fixture response: %v", err)
		}
	})

	// Assembled at runtime so secret scanners don't flag the fixture as a
	// real credential.
	leakedToken := "ghp_" + strings.Repeat("16C7e42F29", 4)
	resp, err := client.Complete(context.Background(), model.Request{
		System:         "be brief",
		Messages:       []model.Message{{Role: "user", Content: "token " + leakedToken + " leaked"}},
		SecretStripper: NewSecretStripper(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "hello" || resp.InputTokens != 12 || resp.OutputTokens != 3 {
		t.Fatalf("response mapping wrong: %+v", resp)
	}
	if resp.ServedModel != "claude-served-x" {
		t.Fatalf("ServedModel not decoded from the response's own model field: %q", resp.ServedModel)
	}
	if gotKey != "test-key" || gotVersion != anthropicAPIVersion {
		t.Fatalf("auth headers wrong: key=%q version=%q", gotKey, gotVersion)
	}
	if strings.Contains(string(received), "ghp_") {
		t.Fatalf("secret reached the wire: %s", received)
	}
	var wire struct {
		Model     string `json:"model"`
		MaxTokens int    `json:"max_tokens"`
	}
	if err := json.Unmarshal(received, &wire); err != nil {
		t.Fatalf("wire body not JSON after stripping: %v", err)
	}
	if wire.Model != "claude-test" {
		t.Fatalf("default model not applied: %q", wire.Model)
	}
	if wire.MaxTokens != anthropicMaxTokensDefault {
		t.Fatalf("max_tokens default not applied: %d", wire.MaxTokens)
	}
}

func TestAnthropicErrorNamesTypeNotKey(t *testing.T) {
	client := newAnthropicForTest(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"type":"authentication_error","message":"invalid x-api-key"}}`)
	})
	_, err := client.Complete(context.Background(), model.Request{Messages: []model.Message{{Role: "user", Content: "hi"}}})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "authentication_error") || !strings.Contains(err.Error(), "401") {
		t.Fatalf("error lacks API type/status: %v", err)
	}
	if strings.Contains(err.Error(), "test-key") {
		t.Fatalf("error leaks the api key: %v", err)
	}
}

func TestAnthropicStreamYieldsDeltas(t *testing.T) {
	client := newAnthropicForTest(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, strings.Join([]string{
			`event: message_start`,
			`data: {"type":"message_start"}`,
			``,
			`event: content_block_delta`,
			`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"Hel"}}`,
			``,
			`event: content_block_delta`,
			`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"lo"}}`,
			``,
			`event: message_stop`,
			`data: {"type":"message_stop"}`,
			``,
		}, "\n"))
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
	if got.String() != "Hello" {
		t.Fatalf("stream text mismatch: %q", got.String())
	}
}

// A large completion rides the SSE wire (completeStreamed); the served model
// identity arrives on message_start, before any content — the assembled
// Response must carry it through just as the non-streaming path does.
func TestAnthropicStreamedCompleteSetsServedModelFromMessageStart(t *testing.T) {
	client := newAnthropicForTest(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, strings.Join([]string{
			`event: message_start`,
			`data: {"type":"message_start","message":{"model":"claude-served-stream"}}`,
			``,
			`event: content_block_delta`,
			`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"hi"}}`,
			``,
			`event: message_delta`,
			`data: {"type":"message_delta","usage":{"output_tokens":1}}`,
			``,
			`event: message_stop`,
			`data: {"type":"message_stop"}`,
			``,
		}, "\n"))
	})
	resp, err := client.Complete(context.Background(), model.Request{
		MaxTokens: streamedCompleteThreshold + 1,
		Messages:  []model.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "hi" {
		t.Fatalf("streamed text mismatch: %q", resp.Text)
	}
	if resp.ServedModel != "claude-served-stream" {
		t.Fatalf("ServedModel not decoded from message_start: %q", resp.ServedModel)
	}
}

func TestAnthropicEmbedIsADifferentLane(t *testing.T) {
	client := newAnthropicForTest(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("no HTTP call may happen for an unsupported lane")
	})
	_, err := client.Embed(context.Background(), model.EmbedRequest{Inputs: []string{"x"}})
	if !errors.Is(err, model.ErrEmbeddingsUnsupported) {
		t.Fatalf("expected ErrEmbeddingsUnsupported, got %v", err)
	}
}
