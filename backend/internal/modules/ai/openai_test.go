// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

func newOpenAIForTest(t *testing.T, handler http.HandlerFunc) *openaiClient {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &openaiClient{http: &http.Client{}, baseURL: srv.URL, apiKey: "sk", defaultModel: "gpt-x"}
}

func TestOpenAICompleteMapsResponsesAPIUsageAndReasoning(t *testing.T) {
	var body []byte
	client := newOpenAIForTest(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Errorf("path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer sk" {
			t.Errorf("auth %s", r.Header.Get("Authorization"))
		}
		body = readBody(t, r.Body)
		// Leading reasoning item BEFORE the message — the parser must walk output[].
		_, _ = w.Write([]byte(`{"id":"resp_1","output":[
			{"type":"reasoning","summary":[]},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}],
			"usage":{"input_tokens":10,"output_tokens":5,
			"output_tokens_details":{"reasoning_tokens":4},
			"input_tokens_details":{"cached_tokens":6}}}`))
	})
	resp, err := client.Complete(context.Background(), model.Request{
		Messages:        []model.Message{{Role: "user", Content: "x"}},
		ProviderOptions: map[string]json.RawMessage{"openai": json.RawMessage(`{"reasoning_effort":"low"}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "hi" || resp.ReasoningTokens != 4 || resp.CachedTokens != 6 {
		t.Fatalf("mapping wrong: %+v", resp)
	}
	if resp.InputTokens != 10 || resp.OutputTokens != 5 {
		t.Fatalf("token mapping wrong: %+v", resp)
	}
	if !bytes.Contains(body, []byte(`"effort":"low"`)) {
		t.Fatalf("reasoning effort not on wire: %s", body)
	}
	// The response id rides ProviderMetadata for session logging.
	if meta := resp.ProviderMetadata["openai"]; !bytes.Contains(meta, []byte("resp_1")) {
		t.Fatalf("response id not surfaced: %s", meta)
	}
}

func TestOpenAISendsStrictJSONSchemaUnderTextFormat(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}}}`)
	var body []byte
	client := newOpenAIForTest(t, func(w http.ResponseWriter, r *http.Request) {
		body = readBody(t, r.Body)
		_, _ = w.Write([]byte(`{"id":"r","output":[{"type":"message","content":[{"type":"output_text","text":"{}"}]}]}`))
	})
	if _, err := client.Complete(context.Background(), model.Request{
		Messages:       []model.Message{{Role: "user", Content: "hi"}},
		ResponseSchema: schema,
	}); err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Text struct {
			Format struct {
				Type   string          `json:"type"`
				Name   string          `json:"name"`
				Schema json.RawMessage `json:"schema"`
				Strict bool            `json:"strict"`
			} `json:"format"`
		} `json:"text"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Text.Format.Type != "json_schema" || wire.Text.Format.Name == "" || !wire.Text.Format.Strict {
		t.Fatalf("text.format shape wrong: %+v", wire.Text.Format)
	}
	if !bytes.Equal(bytes.TrimSpace(wire.Text.Format.Schema), bytes.TrimSpace(schema)) {
		t.Fatalf("schema not verbatim: %s", wire.Text.Format.Schema)
	}
}

func TestOpenAIStripsSecretsFromWire(t *testing.T) {
	var body []byte
	client := newOpenAIForTest(t, func(w http.ResponseWriter, r *http.Request) {
		body = readBody(t, r.Body)
		_, _ = w.Write([]byte(`{"id":"r","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`))
	})
	if _, err := client.Complete(context.Background(), model.Request{
		Messages:       []model.Message{{Role: "user", Content: "with password=verysecretpw inside"}},
		SecretStripper: NewSecretStripper(),
	}); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(body, []byte("verysecretpw")) {
		t.Fatalf("secret reached the wire: %s", body)
	}
}

func TestOpenAIMapsPDFAttachmentToInputFilePart(t *testing.T) {
	var body []byte
	client := newOpenAIForTest(t, func(w http.ResponseWriter, r *http.Request) {
		body = readBody(t, r.Body)
		_, _ = w.Write([]byte(`{"id":"r","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`))
	})
	if _, err := client.Complete(context.Background(), model.Request{
		Messages:    []model.Message{{Role: "user", Content: "read this"}},
		Attachments: []model.Attachment{{MIME: "application/pdf", Bytes: []byte("%PDF"), Name: "contract.pdf"}},
	}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte(`"input_file"`)) || !bytes.Contains(body, []byte("contract.pdf")) {
		t.Fatalf("PDF did not map to an input_file part: %s", body)
	}
}

func TestOpenAIRefusalIsAnError(t *testing.T) {
	client := newOpenAIForTest(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"r","output":[{"type":"message","content":[{"type":"refusal","refusal":"cannot help"}]}]}`))
	})
	_, err := client.Complete(context.Background(), model.Request{Messages: []model.Message{{Role: "user", Content: "x"}}})
	if err == nil || !strings.Contains(err.Error(), "refus") {
		t.Fatalf("want a refusal error, got %v", err)
	}
}

func TestOpenAIEmbedReturnsVectors(t *testing.T) {
	client := newOpenAIForTest(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			t.Errorf("wrong path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1,0.2,0.3]},{"embedding":[0.4,0.5,0.6]}]}`))
	})
	res, err := client.Embed(context.Background(), model.EmbedRequest{Inputs: []string{"a", "b"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Dims != 3 || len(res.Vectors) != 2 {
		t.Fatalf("unexpected shape: %+v", res)
	}
}

func TestOpenAIReportsNotLocalOnly(t *testing.T) {
	client, err := SelectBrain(ProviderConfig{Provider: "openai", APIKey: "k", Model: "gpt-x"})
	if err != nil {
		t.Fatal(err)
	}
	if client.Caps().LocalOnly {
		t.Fatal("openai is a cloud provider — LocalOnly must be false")
	}
}

func TestOpenAIFailsClosedWithoutKey(t *testing.T) {
	if _, err := SelectBrain(ProviderConfig{Provider: "openai"}); err == nil || !strings.Contains(err.Error(), "api key") {
		t.Fatalf("openai without a key must fail closed, got %v", err)
	}
}

// The OpenAI-wire transport appends "/v1/…" to the base, so a default base that
// already carried "/v1" would double it (…/v1/v1/responses → 404). Guards the
// version-less convention shared with Anthropic and vLLM.
func TestOpenAIWireBaseDefaultsAreVersionless(t *testing.T) {
	for name, base := range map[string]string{"openai": defaultOpenAIBaseURL, "vllm": defaultVLLMBaseURL} {
		if strings.HasSuffix(base, "/v1") {
			t.Fatalf("%s default base %q must not end in /v1 — the transport adds it", name, base)
		}
	}
}
