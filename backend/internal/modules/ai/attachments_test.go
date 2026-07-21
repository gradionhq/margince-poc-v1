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
	t.Setenv("OPENAI_COMPATIBLE_API_KEY", "k") // openai_compatible reads its key from env
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()

	// These adapters carry no attachment parts on their wire, so BOTH a document
	// and an image must be rejected — accepting an image the wire can't carry
	// would be a silent drop (the failure this test exists to prevent).
	cannotCarryAttachments := map[string]ProviderConfig{
		"openai_compatible": {Provider: "openai_compatible", BaseURL: srv.URL, Model: "m"},
		"ollama":            {Provider: "ollama", Model: "m", BaseURL: srv.URL},
		"vllm":              {Provider: "vllm", Model: "m", BaseURL: srv.URL},
	}
	for _, att := range []model.Attachment{
		{MIME: "application/pdf", Bytes: []byte("%PDF")},
		{MIME: "image/png", Bytes: []byte("PNG")},
	} {
		for name, cfg := range cannotCarryAttachments {
			t.Run(name+"/"+att.MIME, func(t *testing.T) {
				client, err := SelectBrain(cfg)
				if err != nil {
					t.Fatal(err)
				}
				_, err = client.Complete(context.Background(), model.Request{
					Messages:    []model.Message{{Role: "user", Content: "read this"}},
					Attachments: []model.Attachment{att},
				})
				if !errors.Is(err, model.ErrAttachmentUnsupported) {
					t.Fatalf("%s: want ErrAttachmentUnsupported for %s, got %v", name, att.MIME, err)
				}
			})
		}
	}
}

// An attachment must carry exactly one of inline bytes or a URI; both-set or
// neither-set is a malformed part the gate rejects (spec's Bytes XOR URI).
func TestAttachmentBytesXorURIEnforced(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "k")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"r","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`))
	}))
	defer srv.Close()
	client, err := SelectBrain(ProviderConfig{Provider: "openai", BaseURL: srv.URL, Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range map[string]model.Attachment{
		"both set":    {MIME: "application/pdf", Bytes: []byte("x"), URI: "file-1"},
		"neither set": {MIME: "application/pdf"},
	} {
		_, err := client.Complete(context.Background(), model.Request{
			Messages:    []model.Message{{Role: "user", Content: "x"}},
			Attachments: []model.Attachment{bad},
		})
		if err == nil || !strings.Contains(err.Error(), "exactly one") {
			t.Fatalf("malformed attachment must be rejected, got %v", err)
		}
	}
}

// The native adapters carry documents — a PDF must NOT be rejected. Pairs with
// the rejection fitness test above so "who can ingest this document" stays an
// honest, tested routing input (spec §3.8).
func TestNativeCloudProvidersCarryPDFAttachments(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("GEMINI_API_KEY", "k")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, ":generateContent") {
			_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"r","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`))
	}))
	defer srv.Close()

	pdf := model.Attachment{MIME: "application/pdf", Bytes: []byte("%PDF")}
	canCarryPDF := map[string]ProviderConfig{
		"openai": {Provider: "openai", BaseURL: srv.URL, Model: "m"},
		"gemini": {Provider: "gemini", BaseURL: srv.URL, Model: "m"},
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
