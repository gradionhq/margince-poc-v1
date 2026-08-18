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

// writeFixture writes a canned provider response, failing the test when the
// write itself fails. A discarded Write turns a broken transport into a decode
// error raised by the code under test, which is the wrong thing to read.
func writeFixture(t *testing.T, w http.ResponseWriter, body string) {
	t.Helper()
	if _, err := w.Write([]byte(body)); err != nil {
		t.Errorf("writing the fixture response: %v", err)
	}
}

func TestEveryProviderMapsOrRejectsAttachmentsNeverSilentlyDrops(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeFixture(t, w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer srv.Close()

	// What each binding must REFUSE, and every binding refuses something.
	// The two OpenAI-compatible ones declare no `input:` here, so both kinds are
	// outside their carriage; ollama carries images from code and still refuses
	// the document kind its wire cannot spell at all; anthropic carries both
	// kinds and refuses what no adapter here carries. Accepting a MIME nothing
	// will carry is the silent drop this test exists to prevent.
	mustRefuse := map[string]struct {
		cfg   ProviderConfig
		mimes []string
	}{
		"openai_compatible": {
			cfg:   ProviderConfig{Provider: "openai_compatible", BaseURL: srv.URL, Model: "m"},
			mimes: []string{"application/pdf", "image/png"},
		},
		"vllm": {
			cfg:   ProviderConfig{Provider: "vllm", Model: "m", BaseURL: srv.URL},
			mimes: []string{"application/pdf", "image/png"},
		},
		"ollama":    {cfg: ProviderConfig{Provider: "ollama", Model: "m", BaseURL: srv.URL}, mimes: []string{"application/pdf"}},
		"anthropic": {cfg: ProviderConfig{Provider: "anthropic", Model: "m", BaseURL: srv.URL}, mimes: []string{"audio/mpeg"}},
	}
	for name, tc := range mustRefuse {
		for _, mime := range tc.mimes {
			t.Run(name+"/"+mime, func(t *testing.T) {
				client, err := SelectBrain(tc.cfg, allCloudKeys())
				if err != nil {
					t.Fatal(err)
				}
				_, err = client.Complete(context.Background(), model.Request{
					Messages:    []model.Message{{Role: "user", Content: "read this"}},
					Attachments: []model.Attachment{{MIME: mime, Bytes: []byte("bytes")}},
				})
				if !errors.Is(err, model.ErrAttachmentUnsupported) {
					t.Fatalf("%s: want ErrAttachmentUnsupported for %s, got %v", name, mime, err)
				}
			})
		}
	}
}

// The carriage arm for the two wires whose attachment support is a property of
// the WIRE rather than of the binding: each maps a part in its own spelling, and
// "accepted" is only proved by the part arriving on the wire — a call that
// dropped it would look identical from the caller's side.
func TestAnthropicAndOllamaCarryAttachmentsInTheirOwnWireSpelling(t *testing.T) {
	var sent []byte
	var readErr error
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Kept and asserted rather than dropped: a transport read failure here
		// would otherwise surface as an unmarshal error below and read as a
		// serialization bug in the code under test.
		sent, readErr = io.ReadAll(r.Body)
		if strings.HasPrefix(r.URL.Path, "/v1/messages") {
			writeFixture(t, w, `{"model":"m","content":[{"type":"text","text":"ok"}]}`)
			return
		}
		writeFixture(t, w, `{"model":"m","message":{"content":"ok"},"done":true}`)
	}))
	defer srv.Close()

	png := model.Attachment{MIME: "image/png", Bytes: []byte("PNG")}
	for name, tc := range map[string]struct {
		cfg ProviderConfig
		att model.Attachment
		// wants is the literal fragment the mapped attachment must produce on
		// that wire. Literal, because the spellings are the point: an anthropic
		// base64 source and an ollama bare-base64 images entry are not
		// interchangeable, and a shared assertion would pass on either.
		wants string
	}{
		"anthropic": {
			cfg:   ProviderConfig{Provider: "anthropic", BaseURL: srv.URL, Model: "m"},
			att:   png,
			wants: `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"UE5H"}}`,
		},
		// The same wire, the other block kind: a PDF that arrived as an image
		// block would be refused by the vendor, so the block type is as much
		// part of "carried" as the bytes are.
		"anthropic/pdf": {
			cfg:   ProviderConfig{Provider: "anthropic", BaseURL: srv.URL, Model: "m"},
			att:   model.Attachment{MIME: "application/pdf", Bytes: []byte("%PDF")},
			wants: `{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"JVBERg=="}}`,
		},
		"ollama": {
			cfg:   ProviderConfig{Provider: "ollama", BaseURL: srv.URL, Model: "m"},
			att:   png,
			wants: `"images":["UE5H"]`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			client, err := SelectBrain(tc.cfg, allCloudKeys())
			if err != nil {
				t.Fatal(err)
			}
			sent, readErr = nil, nil
			if _, err := client.Complete(context.Background(), model.Request{
				Messages:    []model.Message{{Role: "user", Content: "read this"}},
				Attachments: []model.Attachment{tc.att},
			}); err != nil {
				t.Fatalf("%s must carry a %s, got %v", name, tc.att.MIME, err)
			}
			if readErr != nil {
				t.Fatalf("reading the request body: %v", readErr)
			}
			if !strings.Contains(string(sent), tc.wants) {
				t.Fatalf("the accepted attachment never reached the wire as %s\n got: %s", tc.wants, sent)
			}
		})
	}
}

// Caps() and the send-time gate are one answer on these two wires as well: a
// caller picks its lane off Caps(), so a provider advertising more than its gate
// admits sends the caller down a lane that cannot work.
func TestAnthropicAndOllamaAdvertiseWhatTheyCarry(t *testing.T) {
	for name, tc := range map[string]struct {
		cfg  ProviderConfig
		want []string
	}{
		"anthropic": {cfg: ProviderConfig{Provider: "anthropic", Model: "m"}, want: carriesImagesAndPDF},
		"ollama":    {cfg: ProviderConfig{Provider: "ollama", Model: "m"}, want: carriesImages},
	} {
		t.Run(name, func(t *testing.T) {
			client, err := SelectBrain(tc.cfg, allCloudKeys())
			if err != nil {
				t.Fatal(err)
			}
			if got := client.Caps().AttachmentMIMEs; !slices.Equal(got, tc.want) {
				t.Fatalf("Caps().AttachmentMIMEs = %v, want %v", got, tc.want)
			}
		})
	}
}

// The other arm of the same invariant: a binding that DECLARES `input: image`
// must carry an image, and must still reject a PDF. Declaring is what separates
// the two arms — the wire, the adapter and the code are identical, so a bug that
// let the declaration decide nothing would leave the rejection arm above passing.
func TestDeclaredImageCarriageAcceptsImagesAndStillRejectsPDFs(t *testing.T) {
	// The body is read, not discarded: "accepted" only means the gate let the
	// call through, and a request that then dropped the attachment would look
	// exactly the same from here. Asserting the part reached the wire is what
	// makes this the map-or-reject test its name claims.
	var sent []byte
	var readErr error
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Kept and asserted rather than dropped: a transport read failure here
		// would otherwise surface as an unmarshal error below and read as a
		// serialization bug in the code under test.
		sent, readErr = io.ReadAll(r.Body)
		writeFixture(t, w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer srv.Close()

	declaresImages := map[string]ProviderConfig{
		"openai_compatible": {Provider: "openai_compatible", BaseURL: srv.URL, Model: "m", Input: []string{"text", "image"}},
		"vllm":              {Provider: "vllm", BaseURL: srv.URL, Model: "m", Input: []string{"text", "image"}},
	}
	for name, cfg := range declaresImages {
		t.Run(name, func(t *testing.T) {
			client, err := SelectBrain(cfg, allCloudKeys())
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
			sent, readErr = nil, nil
			if err := ask(model.Attachment{MIME: "image/png", Bytes: []byte("PNG")}); err != nil {
				t.Fatalf("a binding declaring image must carry image/png, got %v", err)
			}
			if readErr != nil {
				t.Fatalf("reading the request body: %v", readErr)
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
			}, allCloudKeys())
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeFixture(t, w, `{"id":"r","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`)
	}))
	defer srv.Close()
	client, err := SelectBrain(ProviderConfig{Provider: "openai", BaseURL: srv.URL, Model: "m"}, allCloudKeys())
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, ":generateContent") {
			writeFixture(t, w, `{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`)
			return
		}
		writeFixture(t, w, `{"id":"r","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`)
	}))
	defer srv.Close()

	pdf := model.Attachment{MIME: "application/pdf", Bytes: []byte("%PDF")}
	canCarryPDF := map[string]ProviderConfig{
		"openai": {Provider: "openai", BaseURL: srv.URL, Model: "m"},
		"gemini": {Provider: "gemini", BaseURL: srv.URL, Model: "m"},
	}
	for name, cfg := range canCarryPDF {
		t.Run(name, func(t *testing.T) {
			client, err := SelectBrain(cfg, allCloudKeys())
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
