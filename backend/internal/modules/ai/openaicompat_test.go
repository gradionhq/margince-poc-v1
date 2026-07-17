// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"context"
	"net/http"
	"net/http/httptest"
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
