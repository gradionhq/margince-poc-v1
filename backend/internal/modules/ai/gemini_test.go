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

func newGeminiForTest(t *testing.T, handler http.HandlerFunc) *geminiClient {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &geminiClient{http: &http.Client{}, baseURL: srv.URL, apiKey: "gk", defaultModel: "gemini-x"}
}

func TestGeminiCompleteMapsNativeWireAndUsage(t *testing.T) {
	var body []byte
	client := newGeminiForTest(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/models/gemini-x:generateContent") {
			t.Errorf("path %s", r.URL.Path)
		}
		if r.Header.Get("x-goog-api-key") != "gk" {
			t.Errorf("api key header %q", r.Header.Get("x-goog-api-key"))
		}
		body = readBody(t, r.Body)
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"answer"}]}}],
			"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"cachedContentTokenCount":6,"thoughtsTokenCount":4}}`))
	})
	resp, err := client.Complete(context.Background(), model.Request{
		System:   "be terse",
		Messages: []model.Message{{Role: "user", Content: "q"}, {Role: "assistant", Content: "prior"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "answer" || resp.InputTokens != 10 || resp.OutputTokens != 5 || resp.CachedTokens != 6 || resp.ReasoningTokens != 4 {
		t.Fatalf("mapping wrong: %+v", resp)
	}
	var wire struct {
		Contents []struct {
			Role  string `json:"role"`
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"contents"`
		SystemInstruction struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"systemInstruction"` //nolint:tagliatelle // Google's wire format (camelCase)
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.SystemInstruction.Parts[0].Text != "be terse" {
		t.Fatalf("system not mapped to systemInstruction: %s", body)
	}
	if len(wire.Contents) != 2 || wire.Contents[0].Role != "user" || wire.Contents[1].Role != "model" {
		t.Fatalf("roles not mapped (assistant→model): %+v", wire.Contents)
	}
}

func TestGeminiStructuredOutputUsesResponseJSONSchema(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}}}`)
	var body []byte
	client := newGeminiForTest(t, func(w http.ResponseWriter, r *http.Request) {
		body = readBody(t, r.Body)
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"{}"}]}}]}`))
	})
	if _, err := client.Complete(context.Background(), model.Request{
		Messages:       []model.Message{{Role: "user", Content: "hi"}},
		ResponseSchema: schema,
	}); err != nil {
		t.Fatal(err)
	}
	var wire struct {
		GenerationConfig struct {
			ResponseMimeType   string          `json:"responseMimeType"`   //nolint:tagliatelle // Google's wire format (camelCase)
			ResponseJSONSchema json.RawMessage `json:"responseJsonSchema"` //nolint:tagliatelle // Google's wire format (camelCase)
		} `json:"generationConfig"` //nolint:tagliatelle // Google's wire format (camelCase)
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.GenerationConfig.ResponseMimeType != "application/json" {
		t.Fatalf("responseMimeType not set: %s", body)
	}
	if !bytes.Equal(bytes.TrimSpace(wire.GenerationConfig.ResponseJSONSchema), bytes.TrimSpace(schema)) {
		t.Fatalf("responseJsonSchema not verbatim: %s", wire.GenerationConfig.ResponseJSONSchema)
	}
}

func TestGeminiThinkingLevelFromProviderOptions(t *testing.T) {
	var body []byte
	client := newGeminiForTest(t, func(w http.ResponseWriter, r *http.Request) {
		body = readBody(t, r.Body)
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"ok"}]}}]}`))
	})
	if _, err := client.Complete(context.Background(), model.Request{
		Messages:        []model.Message{{Role: "user", Content: "hi"}},
		ProviderOptions: map[string]json.RawMessage{"gemini": json.RawMessage(`{"thinking_level":"low"}`)},
	}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte(`"thinkingLevel":"low"`)) {
		t.Fatalf("thinkingLevel not on wire: %s", body)
	}
}

func TestGeminiMapsImageAndPDFAttachmentsToInlineData(t *testing.T) {
	var body []byte
	client := newGeminiForTest(t, func(w http.ResponseWriter, r *http.Request) {
		body = readBody(t, r.Body)
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"ok"}]}}]}`))
	})
	if _, err := client.Complete(context.Background(), model.Request{
		Messages: []model.Message{{Role: "user", Content: "read these"}},
		Attachments: []model.Attachment{
			{MIME: "image/png", Bytes: []byte("PNG")},
			{MIME: "application/pdf", Bytes: []byte("%PDF")},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte(`"inlineData"`)) || !bytes.Contains(body, []byte(`"image/png"`)) || !bytes.Contains(body, []byte(`"application/pdf"`)) {
		t.Fatalf("attachments not mapped to inlineData: %s", body)
	}
}

func TestGeminiStripsSecretsFromWire(t *testing.T) {
	var body []byte
	client := newGeminiForTest(t, func(w http.ResponseWriter, r *http.Request) {
		body = readBody(t, r.Body)
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"ok"}]}}]}`))
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

func TestGeminiEmbedReturnsVectors(t *testing.T) {
	client := newGeminiForTest(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, ":embedContent") {
			t.Errorf("wrong path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"embedding":{"values":[0.1,0.2,0.3]}}`))
	})
	res, err := client.Embed(context.Background(), model.EmbedRequest{Inputs: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Dims != 3 || len(res.Vectors) != 1 {
		t.Fatalf("unexpected shape: %+v", res)
	}
}

func TestGeminiReportsNotLocalOnly(t *testing.T) {
	client, err := SelectBrain(ProviderConfig{Provider: "gemini", APIKey: "k", Model: "gemini-x"})
	if err != nil {
		t.Fatal(err)
	}
	if client.Caps().LocalOnly {
		t.Fatal("gemini is a cloud provider — LocalOnly must be false")
	}
}

func TestGeminiFailsClosedWithoutKey(t *testing.T) {
	if _, err := SelectBrain(ProviderConfig{Provider: "gemini"}); err == nil || !strings.Contains(err.Error(), "api key") {
		t.Fatalf("gemini without a key must fail closed, got %v", err)
	}
}

func TestGeminiThoughtSignatureRoundTrips(t *testing.T) {
	// (1) A response carrying a thoughtSignature on the model part surfaces it in ProviderMetadata.
	var reqBody []byte
	client := newGeminiForTest(t, func(w http.ResponseWriter, r *http.Request) {
		reqBody = readBody(t, r.Body)
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[
			{"text":"answer","thoughtSignature":"SIG-abc"}]}}],
			"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2}}`))
	})
	resp, err := client.Complete(context.Background(), model.Request{Messages: []model.Message{{Role: "user", Content: "q1"}}})
	if err != nil {
		t.Fatal(err)
	}
	meta := resp.ProviderMetadata["gemini"]
	if !bytes.Contains(meta, []byte("SIG-abc")) {
		t.Fatalf("thought signature not surfaced in ProviderMetadata: %s", meta)
	}

	// (2) On the NEXT call, a signature passed back in ProviderOptions is echoed onto the model part.
	_, err = client.Complete(context.Background(), model.Request{
		Messages: []model.Message{
			{Role: "user", Content: "q1"},
			{Role: "assistant", Content: "answer"},
			{Role: "user", Content: "q2"},
		},
		ProviderOptions: map[string]json.RawMessage{"gemini": json.RawMessage(`{"thought_signatures":["SIG-abc"]}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(reqBody, []byte(`"thoughtSignature":"SIG-abc"`)) {
		t.Fatalf("thought signature not echoed onto the model turn: %s", reqBody)
	}
}
