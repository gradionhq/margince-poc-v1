// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"slices"
	"strings"
	"testing"
)

// A declaration is validated where the operator is reading the config, never on
// the first document a model is handed. Each rejection below is a way an
// `input:` line could otherwise mean nothing at runtime while looking correct.
func TestRoutingRejectsAnInputDeclarationItCannotHonour(t *testing.T) {
	binding := func(extra string) []byte {
		return []byte(`profile: eu_hosted
tiers:
  premium: {provider: openai_compatible, base_url: https://x, model: m` + extra + `}
embeddings: {provider: openai_compatible, base_url: https://x, model: e}
`)
	}
	for name, tc := range map[string]struct {
		yaml      []byte
		wantParts []string
	}{
		"unknown modality names the accepted set": {
			yaml:      binding(`, input: [text, pdf]`),
			wantParts: []string{`unknown input modality "pdf"`, "accepted: text, image"},
		},
		"a typo does not silently disable the feature": {
			yaml:      binding(`, input: [text, vision]`),
			wantParts: []string{`"vision"`, "accepted: text, image"},
		},
		"a repeat is refused rather than deduped": {
			yaml:      binding(`, input: [text, image, image]`),
			wantParts: []string{`"image"`, "listed twice"},
		},
		"an empty list is not a way to say text-only": {
			yaml:      binding(`, input: []`),
			wantParts: []string{"is empty", "omit the field"},
		},
		"a binding that cannot be given text is not a binding": {
			yaml:      binding(`, input: [image]`),
			wantParts: []string{"must include", `"text"`},
		},
		"a native provider says not-supported, not meaningless": {
			yaml: []byte(`profile: eu_hosted
tiers:
  premium: {provider: gemini, model: m, input: [text, image]}
embeddings: {provider: gemini, model: e}
`),
			wantParts: []string{"not supported on provider", `"gemini"`, "openai_compatible and vllm"},
		},
		"the embeddings lane sends no attachments": {
			yaml: []byte(`profile: eu_hosted
tiers:
  premium: {provider: openai_compatible, base_url: https://x, model: m}
embeddings: {provider: openai_compatible, base_url: https://x, model: e, input: [text, image]}
`),
			wantParts: []string{"embeddings lane takes no `input`"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseRouting(tc.yaml)
			if err == nil {
				t.Fatal("want a startup error, got none")
			}
			for _, part := range tc.wantParts {
				if !strings.Contains(err.Error(), part) {
					t.Errorf("error must contain %q, got %q", part, err)
				}
			}
		})
	}
}

func TestRoutingAcceptsADeclarationAndTranslatesItToCarriage(t *testing.T) {
	cfg, err := ParseRouting([]byte(`profile: eu_hosted
tiers:
  premium: {provider: openai_compatible, base_url: https://x, model: m, input: [text, image]}
  cheap_cloud: {provider: openai_compatible, base_url: https://x, model: c}
embeddings: {provider: openai_compatible, base_url: https://x, model: e}
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Tiers[TierPremium].Input; !slices.Equal(got, []string{"text", "image"}) {
		t.Fatalf("premium input = %v", got)
	}
	if got := cfg.Tiers[TierCheapCloud].Input; got != nil {
		t.Fatalf("an undeclared tier must stay nil, got %v", got)
	}
	if got := carriageFor(cfg.Tiers[TierPremium].Input); !slices.Equal(got, []string{"image/*"}) {
		t.Fatalf("carriage = %v, want [image/*]", got)
	}
	// text is the baseline every chat binding already has, not an attachment
	// kind — declaring it must not widen carriage.
	if got := carriageFor([]string{"text"}); got != nil {
		t.Fatalf("text alone carries nothing, got %v", got)
	}
}

// openAICompatImagePart builds an image_url part for whatever it is handed, and
// what keeps that honest is that `image` is the only modality a binding can
// declare. That is an obligation on the VOCABULARY, not on the builder: add a
// modality carrying application/pdf or audio/* and a document would ship as an
// image_url part — the silent mis-carriage this whole feature exists to prevent.
// So widening modalityCarriage past images fails here, and the builder grows a
// branch before the vocabulary grows the word.
func TestEveryDeclarableCarriageIsImageShaped(t *testing.T) {
	for modality, carried := range modalityCarriage {
		for _, mime := range carried {
			if !isImage(mime) {
				t.Errorf("modality %q declares %q, which openAICompatImagePart would still ship as an image_url part", modality, mime)
			}
			// DocumentMIMEs() answers "could any binding have been handed this"
			// from the code-fixed adapters alone. A declarable media type outside
			// that set would make the certification corpus reject a fixture some
			// binding can in fact be given.
			if !slices.Contains(carriesImagesAndPDF, mime) {
				t.Errorf("modality %q declares %q, which DocumentMIMEs() does not list", modality, mime)
			}
		}
	}
}

// The vocabulary and the carriage map are two halves of one fact. A modality
// accepted by the parser but unknown to the map would validate and then carry
// nothing — a declaration that silently does nothing, which is the failure the
// whole field exists to remove.
func TestEveryAcceptedModalityDeclaresItsCarriage(t *testing.T) {
	for _, modality := range acceptedModalities {
		if _, ok := modalityCarriage[modality]; !ok {
			t.Errorf("modality %q is accepted but maps to no carriage", modality)
		}
	}
	for modality := range modalityCarriage {
		if !slices.Contains(acceptedModalities, modality) {
			t.Errorf("modality %q maps to carriage but is not accepted", modality)
		}
	}
}
