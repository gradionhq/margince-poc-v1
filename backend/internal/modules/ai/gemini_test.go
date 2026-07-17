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

func TestGeminiEmbedReturnsVectorsAndPinsOutputDimensionality(t *testing.T) {
	var body []byte
	client := newGeminiForTest(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, ":embedContent") {
			t.Errorf("wrong path %s", r.URL.Path)
		}
		body = readBody(t, r.Body)
		_, _ = w.Write([]byte(`{"embedding":{"values":[0.1,0.2,0.3]}}`))
	})
	res, err := client.Embed(context.Background(), model.EmbedRequest{Inputs: []string{"a"}, Dimensions: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if res.Dims != 3 || len(res.Vectors) != 1 {
		t.Fatalf("unexpected shape: %+v", res)
	}
	if !bytes.Contains(body, []byte(`"outputDimensionality":1024`)) {
		t.Fatalf("outputDimensionality not sent to match the store column: %s", body)
	}
}

func TestGeminiReportsNotLocalOnly(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "k")
	client, err := SelectBrain(ProviderConfig{Provider: "gemini", Model: "gemini-x"})
	if err != nil {
		t.Fatal(err)
	}
	if client.Caps().LocalOnly {
		t.Fatal("gemini is a cloud provider — LocalOnly must be false")
	}
}

func TestGeminiFailsClosedWithoutKey(t *testing.T) {
	clearCloudKeyEnv(t)
	if _, err := SelectBrain(ProviderConfig{Provider: "gemini"}); err == nil || !strings.Contains(err.Error(), "api key") {
		t.Fatalf("gemini without a key must fail closed, got %v", err)
	}
}

func TestGeminiStreamYieldsPartTextChunks(t *testing.T) {
	client := newGeminiForTest(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, ":streamGenerateContent") || r.URL.RawQuery != "alt=sse" {
			t.Errorf("stream path/query wrong: %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		_, _ = io.WriteString(w, `data: {"candidates":[{"content":{"parts":[{"text":"he"}]}}]}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"candidates":[{"content":{"parts":[{"text":"llo"}]}}]}`+"\n\n")
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
	if got.String() != "hello" {
		t.Fatalf("stream mismatch: %q", got.String())
	}
}

func TestGeminiErrorSurfacesStatusAndMessageOnly(t *testing.T) {
	client := newGeminiForTest(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"status":"INVALID_ARGUMENT","message":"bad request"}}`))
	})
	_, err := client.Complete(context.Background(), model.Request{Messages: []model.Message{{Role: "user", Content: "with password=verysecretpw inside"}}})
	if err == nil || !strings.Contains(err.Error(), "INVALID_ARGUMENT") || !strings.Contains(err.Error(), "bad request") {
		t.Fatalf("want vendor status+message, got %v", err)
	}
	if strings.Contains(err.Error(), "verysecretpw") {
		t.Fatalf("error must not echo the request: %v", err)
	}
}

func TestGeminiMapsAttachmentByURIToFileData(t *testing.T) {
	var body []byte
	client := newGeminiForTest(t, func(w http.ResponseWriter, r *http.Request) {
		body = readBody(t, r.Body)
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"ok"}]}}]}`))
	})
	if _, err := client.Complete(context.Background(), model.Request{
		Messages:    []model.Message{{Role: "user", Content: "look"}},
		Attachments: []model.Attachment{{MIME: "application/pdf", URI: "gs://bucket/contract.pdf"}},
	}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte(`"fileData"`)) || !bytes.Contains(body, []byte("gs://bucket/contract.pdf")) {
		t.Fatalf("URI attachment not carried as fileData: %s", body)
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
