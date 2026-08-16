// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

// The /api/chat message shape, which carries an image in a way no other wire
// here does: a per-message `images` array of BARE base64 — no data: prefix, no
// media type, no typed part beside the text. The body stays a plain string.
//
// That is why this adapter has its own message type rather than sharing one.
// The OpenAI-compatible and Anthropic wires both replace the body with an array
// of typed parts; borrowing either mapping would produce a request Ollama reads
// as one long string of JSON.

import (
	"encoding/base64"

	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

type ollamaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// Images is bare base64, one entry per image, omitted entirely on a turn
	// that carries none — so a text-only request is byte-identical to what this
	// adapter sent before images existed.
	Images []string `json:"images,omitempty"`
}

// ollamaMessages builds the wire's message list, attaching any images to the
// last user turn.
//
// Ollama has no top-level system field, so the system prompt travels as the
// leading turn. Attachments belong to a user turn — the same placement every
// other adapter here uses, because they must not disagree about where in a
// conversation a document sits.
func ollamaMessages(system string, msgs []model.Message, atts []model.Attachment) []ollamaMessage {
	out := make([]ollamaMessage, 0, len(msgs)+1)
	if system != "" {
		out = append(out, ollamaMessage{Role: roleSystem, Content: system})
	}
	for _, m := range msgs {
		out = append(out, ollamaMessage{Role: m.Role, Content: m.Content})
	}
	if len(atts) == 0 {
		return out
	}
	idx := len(out) - 1
	for idx >= 0 && out[idx].Role != roleUser {
		idx--
	}
	if idx < 0 {
		out = append(out, ollamaMessage{Role: roleUser})
		idx = len(out) - 1
	}
	for _, a := range atts {
		// Bare base64: the runner decodes the entry as image bytes, so a data:
		// prefix would be decoded as part of the image and fail there instead of
		// here. Only images reach this — carriage is gated against this binding's
		// own list, carriesImages or a narrowing of it — and only inline bytes do,
		// which ollamaRefuseAttachments enforces.
		out[idx].Images = append(out[idx].Images, base64.StdEncoding.EncodeToString(a.Bytes))
	}
	return out
}

// ollamaRefuseAttachments is the map-or-reject gate for this wire (spec §3.8):
// the carriage check every adapter runs, plus the URI shape this one cannot
// express at all.
//
// The `images` array is bytes, and nothing else — Ollama neither fetches a URL
// nor keeps a file registry a handle could name. An adapter that quietly skipped
// such a part would be the silent drop the invariant exists to forbid, so it is
// refused with the reason.
func ollamaRefuseAttachments(atts []model.Attachment, declared []string) error {
	if err := refuseNarrowedAttachments("ollama", atts, declared, carriesImages); err != nil {
		return err
	}
	for _, a := range atts {
		if a.URI != "" {
			return errUnfetchableAttachmentURI("ollama", "inline bytes only")
		}
	}
	return nil
}
