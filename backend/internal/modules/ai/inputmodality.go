// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

// What a bound model can be GIVEN, declared by the binding rather than by the
// adapter (ai-operational-spec §1.4).
//
// The declaration does two different jobs, depending on the wire under it.
//
// On the OpenAI-compatible adapter it is the WHOLE answer, because that adapter
// is one client pointed at an operator-chosen endpoint: whether an image may be
// sent is a property of WHICH MODEL was bound — an OpenRouter binding carries
// images for a vision model and not for a text-only one, a self-hosted vLLM only
// when the served model is one — and nothing in this package can see that.
//
// On an adapter whose carriage is fixed in its wire it NARROWS: at most what the
// wire carries, at most what was declared (narrowedCarriage). That exists for a
// privacy intent the location ladder cannot express, because `profile:` is
// all-or-nothing for the deployment — keeping scanned invoices out of an
// egressing model while keeping that model for text is per tier and per
// modality. Like `profile:`, it governs what THIS product sends; it is not a
// claim about what the operator's endpoint does with what it receives.
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

// carriageFor turns a validated modality list into the media-type set the
// adapter both advertises and enforces. Nil for an undeclared binding, which on
// the OpenAI-compatible wire is the text-only default: it carries no attachment
// parts and refuses them.
func carriageFor(input []string) []string {
	var carried []string
	for _, modality := range input {
		carried = append(carried, modalityCarriage[modality]...)
	}
	return carried
}

// narrowedCarriage is what an adapter whose carriage is fixed in its WIRE
// reports once the binding has had its say: at most what the wire carries, and
// at most what the operator declared (ai-operational-spec §1.4).
//
// An intersection rather than a replacement, and that is the whole safety
// property: a declaration can take `application/pdf` away from a gemini tier —
// the privacy intent this exists for, keeping scanned invoices out of an
// egressing model while keeping it for text — and can never hand `ollama` a
// document lane its wire does not have. A binding must not be able to make an
// adapter claim carriage it cannot honour.
//
// An undeclared binding keeps the adapter's own answer, which is what every
// native binding had before the field existed.
func narrowedCarriage(wireCarries, input []string) []string {
	if input == nil {
		return wireCarries
	}
	return intersectMIMEs(wireCarries, carriageFor(input))
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
func validateInput(label string, input []string) error {
	if input == nil {
		return nil // undeclared: the adapter's own answer, whatever that is
	}
	if len(input) == 0 {
		return fmt.Errorf("ai: routing config: %s: `input` is empty; omit the field to take whatever the bound provider carries, or write `[%s]` to send it no attachments", label, modalityText)
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
