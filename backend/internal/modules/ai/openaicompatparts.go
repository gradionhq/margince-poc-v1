// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

// The OpenAI-compatible chat wire's message body, which is two JSON shapes
// wearing one name: a bare string for an ordinary turn, an ordered array of
// typed parts once the turn carries an attachment.
//
// Both shapes are spelled here so a text-only request keeps marshalling to the
// bare string it has always sent. Endpoints on this wire are not uniformly
// tolerant of the array form, and a text-only call must not regress to buy an
// image lane — that is the whole reason the marshaller picks rather than the
// struct always emitting parts.

import (
	"encoding/json"

	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

type openAICompatMessage struct {
	Role    string              `json:"role"`
	Content openAICompatContent `json:"content"`
}

// openAICompatContent holds whichever shape the turn needs. Parts wins when
// present; otherwise the body is Text.
type openAICompatContent struct {
	Text  string
	Parts []openAICompatContentPart
}

// MarshalJSON emits the string form for a turn with no parts, which is
// byte-for-byte what a plain `string` field produced before attachments existed.
func (c openAICompatContent) MarshalJSON() ([]byte, error) {
	if len(c.Parts) == 0 {
		return json.Marshal(c.Text)
	}
	return json.Marshal(c.Parts)
}

// openAICompatContentPart is one part of a multi-part turn. Text and ImageURL
// are mutually exclusive and Type says which is set, so both are omitempty.
type openAICompatContentPart struct {
	Type     string                `json:"type"` // "text" | "image_url"
	Text     string                `json:"text,omitempty"`
	ImageURL *openAICompatImageURL `json:"image_url,omitempty"`
}

type openAICompatImageURL struct {
	URL string `json:"url"`
}

// openAICompatMessages builds the wire's message list, attaching any parts to
// the last user turn.
//
// The system prompt leads, same as Ollama's chat shape. Attachments belong to a
// user turn — the same placement openaiInputMessages uses on the Responses API
// wire, because the two adapters must not disagree about where in a conversation
// a document sits.
func openAICompatMessages(system string, msgs []model.Message, atts []model.Attachment) []openAICompatMessage {
	out := make([]openAICompatMessage, 0, len(msgs)+1)
	if system != "" {
		out = append(out, openAICompatMessage{Role: roleSystem, Content: openAICompatContent{Text: system}})
	}
	for _, m := range msgs {
		out = append(out, openAICompatMessage{Role: m.Role, Content: openAICompatContent{Text: m.Content}})
	}
	if len(atts) == 0 {
		return out
	}
	idx := lastIndexOfRole(len(out), func(i int) string { return out[i].Role }, roleUser)
	if idx == -1 {
		out = append(out, openAICompatMessage{Role: roleUser})
		idx = len(out) - 1
	}
	// Promoting the turn's text to a leading part keeps it beside the image
	// rather than losing it: once Parts is non-empty the string form is gone.
	target := &out[idx]
	if target.Content.Text != "" {
		target.Content.Parts = append(target.Content.Parts,
			openAICompatContentPart{Type: "text", Text: target.Content.Text})
	}
	for _, a := range atts {
		target.Content.Parts = append(target.Content.Parts, openAICompatImagePart(a))
	}
	return out
}

// openAICompatImagePart maps one attachment to an image_url part. Only images
// reach it: carriage is decided by attachmentUnsupported against the binding's
// declaration, and `image` is the only modality that declaration can name.
func openAICompatImagePart(a model.Attachment) openAICompatContentPart {
	url := a.URI
	if url == "" {
		url = dataURI(a.MIME, a.Bytes)
	}
	return openAICompatContentPart{Type: "image_url", ImageURL: &openAICompatImageURL{URL: url}}
}

// lastIndexOfRole finds the last item whose role matches, or -1. It takes the
// length and an accessor rather than a slice so the two adapters' differently
// typed message lists share the search without sharing a type — the part
// building below and openai.go's differ, the search does not.
func lastIndexOfRole(n int, roleAt func(int) string, want string) int {
	for i := n - 1; i >= 0; i-- {
		if roleAt(i) == want {
			return i
		}
	}
	return -1
}
