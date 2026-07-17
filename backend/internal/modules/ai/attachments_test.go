// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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
