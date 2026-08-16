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
	"slices"
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

	// None of these bindings declares carriage — ollama because its wire has
	// none, the two OpenAI-compatible ones because they omit `input:` — so BOTH a
	// document and an image must be rejected. Accepting an image nothing will
	// carry is the silent drop this test exists to prevent.
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

// The other arm of the same invariant: a binding that DECLARES `input: image`
// must carry an image, and must still reject a PDF. Declaring is what separates
// the two arms — the wire, the adapter and the code are identical, so a bug that
// let the declaration decide nothing would leave the rejection arm above passing.
func TestDeclaredImageCarriageAcceptsImagesAndStillRejectsPDFs(t *testing.T) {
	t.Setenv("OPENAI_COMPATIBLE_API_KEY", "k")
	// The body is read, not discarded: "accepted" only means the gate let the
	// call through, and a request that then dropped the attachment would look
	// exactly the same from here. Asserting the part reached the wire is what
	// makes this the map-or-reject test its name claims.
	var sent []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()

	declaresImages := map[string]ProviderConfig{
		"openai_compatible": {Provider: "openai_compatible", BaseURL: srv.URL, Model: "m", Input: []string{"text", "image"}},
		"vllm":              {Provider: "vllm", BaseURL: srv.URL, Model: "m", Input: []string{"text", "image"}},
	}
	for name, cfg := range declaresImages {
		t.Run(name, func(t *testing.T) {
			client, err := SelectBrain(cfg)
			if err != nil {
				t.Fatal(err)
			}
			ask := func(att model.Attachment) error {
				_, err := client.Complete(context.Background(), model.Request{
					Messages:    []model.Message{{Role: "user", Content: "read this"}},
					Attachments: []model.Attachment{att},
				})
				return err
			}
			sent = nil
			if err := ask(model.Attachment{MIME: "image/png", Bytes: []byte("PNG")}); err != nil {
				t.Fatalf("a binding declaring image must carry image/png, got %v", err)
			}
			var body struct {
				Messages []struct {
					Role    string
					Content []struct {
						Type     string
						ImageURL struct{ URL string } `json:"image_url"`
					}
				}
			}
			if err := json.Unmarshal(sent, &body); err != nil {
				t.Fatalf("the request body is not the parts shape: %v (%s)", err, sent)
			}
			last := body.Messages[len(body.Messages)-1]
			if len(last.Content) == 0 || last.Content[len(last.Content)-1].Type != "image_url" {
				t.Fatalf("the accepted image never reached the wire: %s", sent)
			}
			if url := last.Content[len(last.Content)-1].ImageURL.URL; !strings.HasPrefix(url, "data:image/png;base64,") {
				t.Errorf("want the image as a data URL, got %q", url)
			}
			err = ask(model.Attachment{MIME: "application/pdf", Bytes: []byte("%PDF")})
			if !errors.Is(err, model.ErrAttachmentUnsupported) {
				t.Fatalf("declaring image must not admit application/pdf, got %v", err)
			}
			// The refusal names the knob that fixes it: on this one adapter the
			// carriage is a config line, so an error that stops at "cannot carry"
			// sends the operator into the source to find out why.
			if !strings.Contains(err.Error(), "input:") {
				t.Errorf("refusal must point at the `input:` declaration, got %q", err)
			}
		})
	}
}

// Caps() and the send-time gate must be the same list, or a binding advertises
// carriage its own wire refuses and a caller picks a lane that cannot work.
func TestDeclaredCarriageIsWhatCapsAdvertises(t *testing.T) {
	t.Setenv("OPENAI_COMPATIBLE_API_KEY", "k")
	for name, tc := range map[string]struct {
		input []string
		want  []string
	}{
		"undeclared is text-only": {input: nil, want: nil},
		"declared image":          {input: []string{"text", "image"}, want: []string{"image/*"}},
		"declared text only":      {input: []string{"text"}, want: nil},
	} {
		t.Run(name, func(t *testing.T) {
			client, err := SelectBrain(ProviderConfig{
				Provider: "openai_compatible", BaseURL: "https://example.invalid", Model: "m", Input: tc.input,
			})
			if err != nil {
				t.Fatal(err)
			}
			if got := client.Caps().AttachmentMIMEs; !slices.Equal(got, tc.want) {
				t.Fatalf("Caps().AttachmentMIMEs = %v, want %v", got, tc.want)
			}
		})
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
