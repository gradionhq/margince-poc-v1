// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
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
