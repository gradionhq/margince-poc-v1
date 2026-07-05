// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
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
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client, err := SelectBrain(ProviderConfig{Provider: "anthropic", Model: "claude-test", BaseURL: srv.URL, APIKey: "test-key"})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestAnthropicCompleteSendsStrippedPayload(t *testing.T) {
	var received []byte
	var gotKey, gotVersion string
	client := newAnthropicForTest(t, func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		gotKey = r.Header.Get("X-Api-Key")
		gotVersion = r.Header.Get("Anthropic-Version")
		if err := json.NewEncoder(w).Encode(map[string]any{
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

func TestAnthropicEmbedIsADifferentLane(t *testing.T) {
	client := newAnthropicForTest(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("no HTTP call may happen for an unsupported lane")
	})
	_, err := client.Embed(context.Background(), model.EmbedRequest{Inputs: []string{"x"}})
	if !errors.Is(err, model.ErrEmbeddingsUnsupported) {
		t.Fatalf("expected ErrEmbeddingsUnsupported, got %v", err)
	}
}
