// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

func TestOpenAICompatSendsBearerWhenKeyedAndOmitsWhenNot(t *testing.T) {
	for _, tc := range []struct {
		name       string
		apiKey     string
		wantHeader string
	}{
		{"keyed cloud", "sk-test", "Bearer sk-test"},
		{"unkeyed local", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotAuth string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotAuth = r.Header.Get("Authorization")
				_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
			}))
			defer srv.Close()
			c := &openAICompatClient{http: &http.Client{}, baseURL: srv.URL, apiKey: tc.apiKey, defaultModel: "m"}
			if _, err := c.Complete(context.Background(), model.Request{Messages: []model.Message{{Role: "user", Content: "hi"}}}); err != nil {
				t.Fatal(err)
			}
			if gotAuth != tc.wantHeader {
				t.Fatalf("Authorization = %q, want %q", gotAuth, tc.wantHeader)
			}
		})
	}
}

func TestOpenAICompatSurfacesHTTPErrorWithoutEchoingRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("upstream boom"))
	}))
	defer srv.Close()
	c := &openAICompatClient{http: &http.Client{}, baseURL: srv.URL, apiKey: "k", defaultModel: "m"}
	_, err := c.Complete(context.Background(), model.Request{
		Messages:       []model.Message{{Role: "user", Content: "with password=verysecretpw inside"}},
		SecretStripper: NewSecretStripper(),
	})
	if err == nil || !strings.Contains(err.Error(), "http 500") {
		t.Fatalf("want http 500 surfaced, got %v", err)
	}
	if strings.Contains(err.Error(), "verysecretpw") {
		t.Fatalf("error must not echo the request: %v", err)
	}
}

func TestOpenAICompatEmptyChoicesIsAnError(t *testing.T) {
	c := &openAICompatClient{http: &http.Client{}, defaultModel: "m", baseURL: newJSONServer(t, `{"choices":[]}`)}
	if _, err := c.Complete(context.Background(), model.Request{Messages: []model.Message{{Role: "user", Content: "hi"}}}); err == nil || !strings.Contains(err.Error(), "no choices") {
		t.Fatalf("want a no-choices error, got %v", err)
	}
}

func newJSONServer(t *testing.T, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// vLLM emits its error at the TOP level ({"object":"error",type,message}), not
// under OpenAI's nested {"error":{…}} — the operator must still see the
// message, not a bare "http 400".
func TestOpenAICompatErrorDecodesVLLMTopLevelShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"object":"error","type":"BadRequestError","message":"dimensions is not supported"}`))
	}))
	defer srv.Close()
	client := &openAICompatClient{http: &http.Client{}, baseURL: srv.URL, localOnly: true, defaultModel: "m"}
	_, err := client.Complete(context.Background(), model.Request{Messages: []model.Message{{Role: "user", Content: "q"}}})
	if err == nil || !strings.Contains(err.Error(), "dimensions is not supported") || !strings.Contains(err.Error(), "BadRequestError") {
		t.Fatalf("want vLLM's top-level type+message, got %v", err)
	}
}

// The generic wire gives no way to know whether the server honors OpenAI's
// `dimensions` matryoshka knob, and vLLM 400s on models that aren't MRL-trained
// — the adapter must not put it on the wire even when the caller pins a width.
func TestOpenAICompatEmbedOmitsDimensions(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body = readBody(t, r.Body)
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1,0.2]}]}`))
	}))
	defer srv.Close()
	client := &openAICompatClient{http: &http.Client{}, baseURL: srv.URL, localOnly: true, defaultModel: "bge-m3"}
	if _, err := client.Embed(context.Background(), model.EmbedRequest{Inputs: []string{"a"}, Dimensions: 1024}); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(body, []byte(`"dimensions"`)) {
		t.Fatalf("dimensions must not reach an openai-compatible/vllm server: %s", body)
	}
}
