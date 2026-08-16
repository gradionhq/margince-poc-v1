// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// The intent the field exists for on a native provider: keep scanned invoices
// out of an egressing model while keeping that model for text. `profile:` is
// all-or-nothing for the deployment, so this is the only per-tier way to say it.
func TestADeclarationNarrowsANativeProvidersCarriage(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "k")
	t.Setenv("ANTHROPIC_API_KEY", "k")
	for name, tc := range map[string]struct {
		cfg  ProviderConfig
		want []string
	}{
		"undeclared gemini keeps its whole wire": {
			cfg:  ProviderConfig{Provider: providerGemini, Model: "m"},
			want: carriesImagesAndPDF,
		},
		"gemini narrowed to images loses the document lane": {
			cfg:  ProviderConfig{Provider: providerGemini, Model: "m", Input: []string{"text", "image"}},
			want: []string{"image/*"},
		},
		"gemini narrowed to text carries nothing": {
			cfg:  ProviderConfig{Provider: providerGemini, Model: "m", Input: []string{"text"}},
			want: []string{},
		},
		"anthropic narrowed to text carries nothing": {
			cfg:  ProviderConfig{Provider: providerAnthropic, Model: "m", Input: []string{"text"}},
			want: []string{},
		},
		// The declaration is a ceiling, not a floor: ollama's wire has no
		// document part, so declaring images cannot conjure one and cannot
		// widen past what the wire spells either.
		"ollama declaring images gets images, no more": {
			cfg:  ProviderConfig{Provider: providerOllama, Model: "m", Input: []string{"text", "image"}},
			want: carriesImages,
		},
	} {
		t.Run(name, func(t *testing.T) {
			client, err := SelectBrain(tc.cfg)
			if err != nil {
				t.Fatal(err)
			}
			if got := client.Caps().AttachmentMIMEs; !slices.Equal(got, tc.want) {
				t.Fatalf("Caps().AttachmentMIMEs = %v, want %v", got, tc.want)
			}
		})
	}
}

// Caps() advertising less is worth nothing if the wire still sends it. The
// send-time gate reads the SAME narrowed list, so a document handed to a
// narrowed tier is refused rather than quietly egressed.
func TestANarrowedBindingRefusesWhatItNoLongerAdvertises(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "k")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`)); err != nil {
			t.Errorf("writing the fixture response: %v", err)
		}
	}))
	defer srv.Close()

	narrowed, err := SelectBrain(ProviderConfig{
		Provider: providerGemini, BaseURL: srv.URL, Model: "m", Input: []string{"text", "image"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ask := func(att model.Attachment) error {
		_, err := narrowed.Complete(context.Background(), model.Request{
			Messages:    []model.Message{{Role: roleUser, Content: "read this"}},
			Attachments: []model.Attachment{att},
		})
		return err
	}
	if err := ask(model.Attachment{MIME: "image/png", Bytes: []byte("PNG")}); err != nil {
		t.Fatalf("an image is still declared and must be carried, got %v", err)
	}
	// The whole point: gemini's wire carries a PDF, and this binding said not to.
	if err := ask(model.Attachment{MIME: "application/pdf", Bytes: []byte("%PDF")}); !errors.Is(err, model.ErrAttachmentUnsupported) {
		t.Fatalf("a narrowed binding must refuse the document lane it gave up, got %v", err)
	}
}

// The safety property, stated as its own test because it is the one thing a
// narrowing must never do. `narrowedCarriage` is an intersection, so no
// declaration can add a media type the wire cannot send — the check is over
// every declarable modality against every adapter's own carriage, rather than
// the one example that happens to be interesting today.
func TestNoDeclarationCanWidenAnAdaptersCarriage(t *testing.T) {
	wires := map[string][]string{
		"native document wire": carriesImagesAndPDF,
		"image-only wire":      carriesImages,
		"text-only wire":       carriesNothing,
	}
	for wireName, wire := range wires {
		for _, input := range [][]string{
			{modalityText},
			{modalityText, modalityImage},
		} {
			got := narrowedCarriage(wire, input)
			for _, mime := range got {
				if !slices.Contains(wire, mime) {
					t.Errorf("%s: declaring %v produced %q, which the wire does not carry", wireName, input, mime)
				}
			}
			if len(got) > len(wire) {
				t.Errorf("%s: declaring %v widened carriage to %v", wireName, input, got)
			}
		}
		// An undeclared binding is the identity: every native binding behaved
		// this way before the field existed and must still.
		if got := narrowedCarriage(wire, nil); !slices.Equal(got, wire) {
			t.Errorf("%s: an undeclared binding must keep the wire's own answer, got %v", wireName, got)
		}
	}
}

// A task's carriage is the intersection over its ladder, so narrowing ONE rung
// narrows the task — which is the point here rather than a footnote: an operator
// keeping scans off one model gets no lane the budget guardrail could demote
// them onto.
func TestNarrowingOneRungNarrowsTheWholeTask(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "k")
	cfg, err := ParseRouting([]byte(`
profile: eu_hosted
tiers:
  cheap_cloud: {provider: gemini, model: m}
  premium: {provider: gemini, model: m, input: [text]}
embeddings: {provider: gemini, model: e}
`))
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter(cfg, nil, nil, nil, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := router.AttachmentMIMEs(TaskColdStart); len(got) != 0 {
		t.Fatalf("one narrowed rung must narrow the task, got %v", got)
	}
}
