// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

// What a bound model can be GIVEN, declared by the binding rather than by the
// adapter (ai-operational-spec §1.4).
//
// Every other provider answers "may I send an image?" from code, because the
// answer is a property of its wire. The OpenAI-compatible adapter cannot: it is
// one client pointed at an operator-chosen endpoint, so the answer is a property
// of WHICH MODEL was bound — an OpenRouter binding carries images for a vision
// model and not for a text-only one, and a self-hosted vLLM carries them only
// when the served model is a vision model. Nothing in this package can see that,
// so the operator states it and the adapter is held to the statement.
//
// A declaration is a claim about the endpoint, not a validated fact. A binding
// that claims more than its model serves fails on the wire — visibly, which is
// the honest failure mode and needs no second guard.

import (
	"fmt"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// The accepted modality words. `text` is every chat binding's baseline and
// carries no attachment; `image` is the one attachment kind this wire spells
// uniformly.
//
// `pdf` is deliberately absent. Images are one shape here — an `image_url`
// content part — while a PDF rides a proprietary request-body extension on one
// vendor and nothing at all on a self-hosted endpoint. One word meaning
// different things per vendor is the silent divergence this declaration exists
// to prevent, so `pdf` is refused by name rather than quietly accepted.
const (
	modalityText  = "text"
	modalityImage = "image"
)

// acceptedModalities is the closed vocabulary, in the order the error message
// lists it. Adding a word here without teaching modalityCarriage what it maps
// to is caught by TestEveryAcceptedModalityDeclaresItsCarriage.
var acceptedModalities = []string{modalityText, modalityImage}

// modalityCarriage maps one modality word to the media types it admits, in
// model.CarriesMIME's spelling. `text` admits none: it is the baseline every
// chat binding already has, not an attachment kind.
var modalityCarriage = map[string][]string{
	modalityText:  nil,
	modalityImage: {"image/*"},
}

// inputProviders are the providers whose carriage is a property of the bound
// model rather than of the adapter, so only they accept an `input:` declaration.
// Both are served by openAICompatClient.
var inputProviders = []string{providerOpenAICompatible, providerVLLM}

// carriageFor turns a validated modality list into the media-type set the
// adapter both advertises and enforces. Nil for an undeclared binding, which is
// the text-only default: it carries no attachment parts and refuses them.
func carriageFor(input []string) []string {
	var carried []string
	for _, modality := range input {
		carried = append(carried, modalityCarriage[modality]...)
	}
	return carried
}

// blankInputDeclarations names every binding that wrote `input:` with no value.
//
// yaml decodes a bare `input:`, an explicit `input: null` and an absent key all
// to the same nil slice, so the parsed config cannot tell "text-only by
// omission" from "I meant to declare something and left it blank". Only the
// second is a mistake, and it is this feature's own failure mode: a declaration
// that reads as present and does nothing. The DOCUMENT still knows the
// difference — a written key is a !!null scalar node, an absent one is no node
// at all — so the answer is read from there.
//
// Decoded without KnownFields on purpose: this pass reads one field and must
// ignore every other, which the caller's own strict decode has already
// validated.
func blankInputDeclarations(raw []byte) ([]string, error) {
	var probe struct {
		Tiers map[Tier]struct {
			Input yaml.Node `yaml:"input"`
		} `yaml:"tiers"`
		Embeddings struct {
			Input yaml.Node `yaml:"input"`
		} `yaml:"embeddings"`
	}
	if err := yaml.Unmarshal(raw, &probe); err != nil {
		return nil, fmt.Errorf("ai: routing config: %w", err)
	}
	written := func(n yaml.Node) bool { return n.Kind != 0 && n.Tag == "!!null" }
	var blank []string
	for tier, binding := range probe.Tiers {
		if written(binding.Input) {
			blank = append(blank, fmt.Sprintf("tier %s", tier))
		}
	}
	if written(probe.Embeddings.Input) {
		blank = append(blank, "the embeddings lane")
	}
	slices.Sort(blank) // map iteration order must not decide which error an operator sees
	return blank, nil
}

// validateInput enforces the declaration rules at STARTUP, where an operator is
// reading the config, rather than on the first document a model is handed.
//
// label names the binding under inspection ("tier premium", "the embeddings
// lane") so an error points at a line rather than at the file.
func validateInput(label, provider string, input []string) error {
	if input == nil {
		return nil // undeclared: text-only, the safe default
	}
	if !slices.Contains(inputProviders, provider) {
		return fmt.Errorf(
			"ai: routing config: %s: `input` is not supported on provider %q yet — there the carriage is fixed in the adapter's own code; it is accepted only on %s, where it depends on which model is served",
			label, provider, strings.Join(inputProviders, " and "))
	}
	if len(input) == 0 {
		return fmt.Errorf("ai: routing config: %s: `input` is empty; omit the field to bind a text-only model", label)
	}
	for i, modality := range input {
		if !slices.Contains(acceptedModalities, modality) {
			return fmt.Errorf("ai: routing config: %s: unknown input modality %q (accepted: %s)",
				label, modality, strings.Join(acceptedModalities, ", "))
		}
		// A repeat is not additive — carriageFor concatenates, so the duplicate
		// reaches Caps(), the ladder intersection and the operator's own error
		// message as a doubled pattern. Refuse the input rather than dedupe it:
		// the operator meant something by writing it twice, and neither reading
		// is one this field has.
		if slices.Contains(input[:i], modality) {
			return fmt.Errorf("ai: routing config: %s: input modality %q is listed twice", label, modality)
		}
	}
	if !slices.Contains(input, modalityText) {
		return fmt.Errorf("ai: routing config: %s: `input` must include %q — a chat binding that cannot be given text is not a binding", label, modalityText)
	}
	return nil
}
