// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

// The Messages API's message body, which is two JSON shapes wearing one name: a
// bare string for an ordinary turn, an ordered array of typed blocks once the
// turn carries an image.
//
// Both shapes are spelled here for the same reason the OpenAI-compatible wire
// spells both (openaicompatparts.go): a text-only call must keep marshalling to
// the bare string it has always sent rather than change shape to buy an image
// lane.

import (
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// The block types this wire spells. Deliberately not modalityText/modalityImage:
// those are the routing config's vocabulary, and one shared constant would turn
// the two happening to coincide into a dependency between a wire format and an
// operator-facing word.
const (
	anthropicBlockText  = "text"
	anthropicBlockImage = "image"
)

type anthropicMessage struct {
	Role    string           `json:"role"`
	Content anthropicContent `json:"content"`
}

// anthropicContent holds whichever shape the turn needs. Blocks win when
// present; otherwise the body is Text.
type anthropicContent struct {
	Text   string
	Blocks []anthropicBlock
}

// MarshalJSON emits the string form for a turn with no blocks, which is
// byte-for-byte what a plain `string` field produced before attachments existed.
func (c anthropicContent) MarshalJSON() ([]byte, error) {
	if len(c.Blocks) == 0 {
		return json.Marshal(c.Text)
	}
	return json.Marshal(c.Blocks)
}

// anthropicBlock is one block of a multi-block turn. Text and Source are
// mutually exclusive and Type says which is set, so both are omitempty.
type anthropicBlock struct {
	Type   string                `json:"type"` // "text" | "image"
	Text   string                `json:"text,omitempty"`
	Source *anthropicImageSource `json:"source,omitempty"`
}

// anthropicImageSource is the image block's source, in either of the two
// spellings the API accepts: inline base64 with its media type, or a URL the
// API fetches. MediaType/Data belong to the first, URL to the second.
type anthropicImageSource struct {
	Type      string `json:"type"` // "base64" | "url"
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
}

// anthropicMessages builds the wire's message list, attaching any images to the
// last user turn.
//
// No system turn: Anthropic carries the system prompt in its own top-level
// field, so unlike the Ollama and OpenAI-compatible shapes this list is the
// conversation alone. Attachments belong to a user turn — the same placement
// every other adapter here uses, because they must not disagree about where in a
// conversation a document sits.
func anthropicMessages(msgs []model.Message, atts []model.Attachment) []anthropicMessage {
	out := make([]anthropicMessage, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, anthropicMessage{Role: m.Role, Content: anthropicContent{Text: m.Content}})
	}
	if len(atts) == 0 {
		return out
	}
	idx := len(out) - 1
	for idx >= 0 && out[idx].Role != roleUser {
		idx--
	}
	if idx < 0 {
		out = append(out, anthropicMessage{Role: roleUser})
		idx = len(out) - 1
	}
	// Promoting the turn's text to a leading block keeps it beside the image
	// rather than losing it: once Blocks is non-empty the string form is gone.
	target := &out[idx]
	if target.Content.Text != "" {
		target.Content.Blocks = append(target.Content.Blocks, anthropicBlock{Type: anthropicBlockText, Text: target.Content.Text})
	}
	for _, a := range atts {
		target.Content.Blocks = append(target.Content.Blocks, anthropicImageBlock(a))
	}
	return out
}

// anthropicImageBlock maps one attachment to an image block. Only images reach
// it: carriage is decided by attachmentUnsupported against carriesImages, which
// admits nothing else.
func anthropicImageBlock(a model.Attachment) anthropicBlock {
	if a.URI != "" {
		return anthropicBlock{Type: anthropicBlockImage, Source: &anthropicImageSource{Type: "url", URL: a.URI}}
	}
	return anthropicBlock{Type: anthropicBlockImage, Source: &anthropicImageSource{
		Type:      "base64",
		MediaType: a.MIME,
		Data:      base64.StdEncoding.EncodeToString(a.Bytes),
	}}
}

// anthropicRefuseAttachments is the map-or-reject gate for this wire
// (spec §3.8): the carriage check every adapter runs, plus the one URI shape
// this wire cannot express.
//
// A URI attachment is a URL or a provider file handle. Anthropic fetches an
// https URL itself, so that half maps. A handle would be its Files API
// (`source.type: "file"`), which the endpoint serves only to a request carrying
// the Files beta header this adapter does not send — so a handle is refused
// rather than mapped to a block the vendor would reject for a reason that names
// the wrong thing.
func anthropicRefuseAttachments(atts []model.Attachment) error {
	if err := attachmentUnsupported("anthropic", atts, carriesImages); err != nil {
		return err
	}
	for _, a := range atts {
		if a.URI != "" && !isFetchableURL(a.URI) {
			return errUnfetchableAttachmentURI("anthropic", "an http(s) url the api fetches itself, or inline bytes")
		}
	}
	return nil
}

// isFetchableURL reports whether a URI is one a vendor that fetches its own
// attachments can resolve, as opposed to a handle scoped to some other provider.
func isFetchableURL(uri string) bool {
	return strings.HasPrefix(uri, "https://") || strings.HasPrefix(uri, "http://")
}
