// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The pre-meeting brief, as the agent surface reads it.
//
// There were two answers to "prepare me for this meeting", and the agent got
// the weaker one. A person opened eight cited sections — goal, attendees,
// commitments, risks, and the rest — while an agent asking the same question
// got the assembled context walk with its open tasks pulled to the front, from
// a code path that shares nothing with the brief. Both were individually
// reasonable, which is exactly why nobody noticed they disagreed.
//
// The brief lives in compose and a module may not import compose (ADR-0054
// §3), so what crosses is one function, bound in compose/meetingbriefseam.go.
// That is the sanctioned shape for this edge and the reason to take it rather
// than reimplement: a second assembler is what produced the split.

import (
	"context"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// MeetingBriefReader assembles the brief for ONE meeting, under the calling
// principal's own scope — the identical read the person page performs.
//
// It answers ErrNotFound for an activity that is not a booked meeting, which
// is what lets the tool route on the anchor without pre-checking the kind.
type MeetingBriefReader func(ctx context.Context, activityID ids.UUID) (MeetingBriefResult, error)

// MeetingBriefResult is the brief in the tool surface's own vocabulary.
//
// Its sentences keep their evidence. An assembled picture summarizes records
// and the envelope names them, and the brief holds itself to the same rule in
// its own words: every sentence is cited or dropped whole. Flattening it to
// prose here would strip precisely what an agent needs to act on what it read.
type MeetingBriefResult struct {
	ActivityID ids.UUID `json:"activity_id"`
	// ProjectID is the body of work the meeting is filed under, when it is
	// filed under one. The brief scopes itself by it, which is what a caller
	// narrowing the prep by project needs to know.
	ProjectID   *ids.UUID          `json:"project_id,omitempty"`
	GeneratedAt string             `json:"generated_at"`
	GeneratedBy string             `json:"generated_by"`
	Sections    []MeetingBriefPart `json:"sections"`
}

// MeetingBriefPart is one heading of the brief with its cited lines.
type MeetingBriefPart struct {
	Kind      string             `json:"kind"`
	Sentences []MeetingBriefLine `json:"sentences"`
}

// MeetingBriefLine is one claim and the records it rests on.
type MeetingBriefLine struct {
	Text string `json:"text"`
	// Nature is "assessment" or "recommendation" when the line is a judgment
	// or an ask; empty means a plain fact, which is the contract's default. An
	// agent that cannot tell a record from a reading of one will repeat the
	// reading as a record.
	Nature   string             `json:"nature,omitempty"`
	Evidence []MeetingBriefCite `json:"evidence"`
}

// MeetingBriefCite is one record a brief line was written from.
//
// Not ContextEvidence: that carries a source string and a snippet, which is
// what an assembled picture has to offer. A brief cites RECORDS — a typed id
// the caller can go and read — and flattening one into a snippet would throw
// away the only part an agent can act on.
type MeetingBriefCite struct {
	RecordType string   `json:"record_type"`
	RecordID   ids.UUID `json:"record_id"`
}
