// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package draftfloor

import (
	"strconv"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/convstate"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/textlang"
)

// Envelope is what every drafting surface is told about the correspondence it
// is writing into, as opposed to what it is writing about.
//
// The fields are pinned by ai-operational-spec.md §2.4 and they are all flat
// strings, which is a constraint rather than a style: the certification harness
// decodes a prompt payload as a string map to bound every field it carries, so
// a nested value here would refuse every draft case at preparation time. It is
// also the reason SilenceDays is a string — the field is rendered into a
// prompt, never arithmetic.
//
// Every field is server-derived. None of it comes from the counterparty's own
// text, which is what keeps an instruction in an inbound message from changing
// who a draft says it is from.
type Envelope struct {
	// Language the draft must be written in (DRAFT-AC-E-1).
	Language string `json:"output_language"`
	// ConversationState is the convstate band (DRAFT-AC-E-3, E-4).
	ConversationState string `json:"conversation_state"`
	// SilenceDays is whole days since the last message either way, empty at
	// band none where there is no last message to count from.
	SilenceDays string `json:"silence_days,omitempty"`
	// Now is the current time, RFC 3339. Without it a model cannot tell two
	// days from eight months apart, which is every time-truthful sentence in a
	// draft (DRAFT-AC-E-5).
	Now string `json:"now"`
	// SenderName and SenderEmail are the acting human's, so the draft is not
	// written as whoever appears in a quoted header. Both empty for a system
	// principal with no human authority behind it, where drafting degrades to
	// an unsigned draft rather than failing (DRAFT-AC-E-6).
	SenderName  string `json:"sender_name,omitempty"`
	SenderEmail string `json:"sender_email,omitempty"`
}

// NewEnvelope assembles the envelope from the resolved facts.
func NewEnvelope(lang textlang.Lang, state convstate.State, now time.Time, senderName, senderEmail string) Envelope {
	envelope := Envelope{
		Language:          string(langOrDefault(lang)),
		ConversationState: string(state.Band),
		Now:               now.UTC().Format(time.RFC3339),
		SenderName:        senderName,
		SenderEmail:       senderEmail,
	}
	if state.Band != convstate.BandNone {
		envelope.SilenceDays = strconv.Itoa(state.SilenceDays)
	}
	return envelope
}

// Lang reads the envelope's language back as a typed value, so a caller
// rendering the floor does not have to re-parse the string it just wrote.
func (e Envelope) Lang() textlang.Lang { return langOrDefault(textlang.Lang(e.Language)) }

// Band reads the envelope's conversation state back as a typed value.
func (e Envelope) Band() convstate.Band { return convstate.Band(e.ConversationState) }

// langOrDefault resolves an unknown or unrecognized language to the default.
// One spelling, so the envelope, the floor table and the prompt cannot disagree
// about what an unresolved language means.
func langOrDefault(lang textlang.Lang) textlang.Lang {
	if _, ok := table[lang]; ok {
		return lang
	}
	return DefaultLang
}
