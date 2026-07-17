// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

func TestEveryProviderMapsOrRejectsAttachmentsNeverSilentlyDrops(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()

	pdf := model.Attachment{MIME: "application/pdf", Bytes: []byte("%PDF")}
	// Every Phase-1 provider that cannot carry a PDF must return the sentinel.
	cannotCarryPDF := map[string]ProviderConfig{
		"openai_compatible": {Provider: "openai_compatible", APIKey: "k", BaseURL: srv.URL, Model: "m"},
		"ollama":            {Provider: "ollama", Model: "m", BaseURL: srv.URL},
		"vllm":              {Provider: "vllm", Model: "m", BaseURL: srv.URL},
	}
	for name, cfg := range cannotCarryPDF {
		t.Run(name, func(t *testing.T) {
			client, err := SelectBrain(cfg)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Complete(context.Background(), model.Request{
				Messages:    []model.Message{{Role: "user", Content: "read this"}},
				Attachments: []model.Attachment{pdf},
			})
			if !errors.Is(err, model.ErrAttachmentUnsupported) {
				t.Fatalf("%s: want ErrAttachmentUnsupported for a PDF, got %v", name, err)
			}
		})
	}
}

// The native adapters carry documents — a PDF must NOT be rejected. Pairs with
// the rejection fitness test above so "who can ingest this document" stays an
// honest, tested routing input (spec §3.8).
func TestNativeCloudProvidersCarryPDFAttachments(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, ":generateContent") {
			_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"ok"}]}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"r","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`))
	}))
	defer srv.Close()

	pdf := model.Attachment{MIME: "application/pdf", Bytes: []byte("%PDF")}
	canCarryPDF := map[string]ProviderConfig{
		"openai": {Provider: "openai", APIKey: "k", BaseURL: srv.URL, Model: "m"},
		"gemini": {Provider: "gemini", APIKey: "k", BaseURL: srv.URL, Model: "m"},
	}
	for name, cfg := range canCarryPDF {
		t.Run(name, func(t *testing.T) {
			client, err := SelectBrain(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.Complete(context.Background(), model.Request{
				Messages:    []model.Message{{Role: "user", Content: "read this"}},
				Attachments: []model.Attachment{pdf},
			}); err != nil {
				t.Fatalf("%s must carry a PDF, got %v", name, err)
			}
		})
	}
}
