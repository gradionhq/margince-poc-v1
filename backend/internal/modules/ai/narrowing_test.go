// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
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

// The safety property, and the one thing a narrowing must never do. Derived
// from knownProviders rather than a hand-kept list of wires: a seventh provider
// — or a sixth spelling its carriage differently, say `image/png` where the
// vocabulary says `image/*` — would intersect to something its operator did not
// ask for, and a list maintained by hand is the thing that would not notice.
func TestNoDeclarationCanWidenAnyProvidersCarriage(t *testing.T) {
	for _, env := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY", "OPENAI_COMPATIBLE_API_KEY"} {
		t.Setenv(env, "k")
	}
	for _, provider := range knownProviders {
		for _, input := range [][]string{{modalityText}, {modalityText, modalityImage}} {
			t.Run(provider+"/"+strings.Join(input, "+"), func(t *testing.T) {
				undeclared := capsFor(t, provider, nil)
				declared := capsFor(t, provider, input)

				// At most what was asked for. True of every provider, including
				// the two where the declaration IS the carriage.
				asked := carriageFor(input)
				for _, mime := range declared {
					if !slices.Contains(asked, mime) {
						t.Errorf("declaring %v got %q, which was not asked for (%v)", input, mime, asked)
					}
				}
				// And at most what the wire has, wherever the wire has an answer
				// of its own. An undeclared binding IS that answer, which is why
				// this needs no list of which providers those are: the two whose
				// carriage is purely declarative report nothing here.
				if len(undeclared) == 0 {
					return
				}
				for _, mime := range declared {
					if !slices.Contains(undeclared, mime) {
						t.Errorf("declaring %v got %q, which %s does not carry undeclared (%v)",
							input, mime, provider, undeclared)
					}
				}
			})
		}
	}
}

// capsFor builds a binding the way production does and reports what it says it
// can be given. Through SelectBrain deliberately: the carriage is decided there,
// so a hand-built client would answer for a configuration that never ships.
func capsFor(t *testing.T, provider string, input []string) []string {
	t.Helper()
	cfg := ProviderConfig{Provider: provider, Model: "m", Input: input}
	if provider == providerOpenAICompatible {
		cfg.BaseURL = "https://example.invalid" // the one provider that requires it
	}
	client, err := SelectBrain(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return client.Caps().AttachmentMIMEs
}

// Which refusal an operator gets is the difference between a dead end and an
// edit: a wire that never carried the type has nothing to change, while a
// binding that gave the lane up is one config line from carrying it again.
func TestANarrowedRefusalNamesTheLineThatCausedIt(t *testing.T) {
	narrowed := refuseNarrowedAttachments("gemini",
		[]model.Attachment{{MIME: "application/pdf", Bytes: []byte("%PDF")}},
		[]string{"image/*"}, carriesImagesAndPDF)
	if !errors.Is(narrowed, model.ErrAttachmentUnsupported) {
		t.Fatalf("a narrowed binding must still refuse with the sentinel, got %v", narrowed)
	}
	if !strings.Contains(narrowed.Error(), "`input:`") {
		t.Errorf("a refusal the operator can undo must name the line, got %q", narrowed)
	}
	// The wire's own refusal is final, and pointing at `input:` there would send
	// an operator after a knob that cannot help them.
	final := refuseNarrowedAttachments("ollama",
		[]model.Attachment{{MIME: "application/pdf", Bytes: []byte("%PDF")}},
		carriesImages, carriesImages)
	if !errors.Is(final, model.ErrAttachmentUnsupported) {
		t.Fatalf("the wire's refusal must still be a refusal, got %v", final)
	}
	if strings.Contains(final.Error(), "`input:`") {
		t.Errorf("a refusal no config line fixes must not name one, got %q", final)
	}
}

// The injected fake stands in for whatever bindings name `fake`, so the
// config's narrowing has to reach it — otherwise the one path that swaps the
// client is the one path where a declaration does nothing.
func TestAnInjectedFakeStillHonoursTheBindingsNarrowing(t *testing.T) {
	cfg, err := ParseRouting([]byte(`
profile: sovereign
tiers:
  local_small: {provider: fake, model: m, input: [text]}
embeddings: {provider: fake, model: e}
`))
	if err != nil {
		t.Fatal(err)
	}
	fake := NewFakeClient()
	if _, err := NewLocalRouter(cfg, WithFakeClient(fake)); err != nil {
		t.Fatal(err)
	}
	if got := fake.Caps().AttachmentMIMEs; len(got) != 0 {
		t.Fatalf("the injected fake must take the binding's narrowing, got %v", got)
	}
	if _, err := fake.Stream(context.Background(), model.Request{
		Messages:    []model.Message{{Role: roleUser, Content: "x"}},
		Attachments: []model.Attachment{{MIME: "image/png", Bytes: []byte("PNG")}},
	}); !errors.Is(err, model.ErrAttachmentUnsupported) {
		// Streaming runs the same gate as Complete: a lane that refuses on one
		// method and carries on the other has a Caps() that answers for neither.
		t.Fatalf("Stream must run the same attachment gate, got %v", err)
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
