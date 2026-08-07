// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// What every tool answers with, as a type.
//
// WHY THESE EXIST. A tool's result used to be a `map[string]any` literal built at
// the return statement, and its advertised OutputSchema was `{"type":"object"}` —
// the only schema a map can honestly claim. The two halves of that agreement were
// never held together by anything: a key could be renamed, added or dropped and
// no gate would notice, because the schema said nothing to contradict.
//
// A named type fixes both halves at once. It is what the handler marshals, so the
// wire shape is the type's; and outputshapes.go derives the declared schema from
// the same type, so the schema is the type's too. Neither can move without the
// other.
//
// TWO TYPES HERE MARSHAL NOTHING, and that is deliberate rather than dead:
// PassthroughEntityResult and RunReportResult describe results another module
// builds. This surface must not re-marshal those — doing
// so would drop whatever the producing entity carries and silently move the
// wire — so the type exists to DECLARE the guaranteed subset, and the
// conformance suite in the integration lane holds each one to what the real
// handler answers with. A declaration nothing checked would be the comment this
// whole change is replacing.
//
// WHAT BELONGS HERE. The shape of a RESULT, and nothing else. These types carry
// no behaviour: they are the wire, written down. A field's json tag is the wire
// name, `omitempty` means genuinely optional, and a pointer means the value can
// be absent rather than zero — all three are read by the generator, so they are
// statements about the contract rather than about Go.
//
// WHERE THE REST ARE. Types that already existed keep their homes, because they
// are already the shape of the thing they name: `wireRecord` (tools.go, the ONE
// place a datasource.Record becomes tool output), `Pipeline`/`Stage`
// (tools_pipelines.go), and the relationship-graph answers (tools_network.go).
// The generator reads all of them; this file is only where the ones that had no
// type until now came to live.

import (
	"encoding/json"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// SearchRecordsResult is what search_records answers: the matching records and,
// when the search was confined to one type and more remain, the cursor that
// resumes it.
type SearchRecordsResult struct {
	Records []wireRecord `json:"records"`
	// NextCursor is absent rather than empty when there is no next page — an
	// empty string reads as a cursor a caller might try to use.
	NextCursor string `json:"next_cursor,omitempty"`
}

// ArchiveResult is what archive_record answers: the record it retired, named the
// way every other tool names one.
type ArchiveResult struct {
	// Archived is always true in a result — a refusal is an error, not a result
	// saying no — and it is on the wire because a caller reading only the result
	// should not have to infer the outcome from the absence of an error.
	Archived   bool                  `json:"archived"`
	RecordType datasource.EntityType `json:"record_type"`
	ID         ids.UUID              `json:"id"`
}

// PromoteLeadResult is what promote_lead answers: the person the lead became,
// and whether that person already existed.
type PromoteLeadResult struct {
	// Merged is true when the promotion landed on an EXISTING person rather than
	// creating one — the caller's follow-up differs, because a merged promotion
	// means the person carries history the lead never had.
	Merged bool       `json:"merged"`
	Person wireRecord `json:"person"`
}

// MergeRecordsResult is what merge_records answers: which record survived.
type MergeRecordsResult struct {
	Merged     bool                  `json:"merged"`
	RecordType datasource.EntityType `json:"record_type"`
	// SurvivorID is the target, never the source: the source is archived and
	// redirected, so an id a caller keeps has to be the one that still resolves.
	SurvivorID ids.UUID `json:"survivor_id"`
}

// DraftEmailResult is what draft_email answers: a message nobody has sent.
type DraftEmailResult struct {
	Subject string `json:"subject"`
	Body    string `json:"body"`
	// InReplyToActivityID is echoed back because send_email needs it, and a
	// caller that had to remember it across the two calls is a caller that can
	// send a draft against the wrong thread.
	InReplyToActivityID ids.UUID `json:"in_reply_to_activity_id"`
}

// ContextAnchor names the record an assembled picture was built around.
type ContextAnchor struct {
	RecordType datasource.EntityType `json:"record_type"`
	RecordID   ids.UUID              `json:"record_id"`
}

// ContextEvidence is one source a summarized item rests on. An item with no
// evidence is not returned at all, so this is never empty in a result.
type ContextEvidence struct {
	Source  string `json:"source"`
	Snippet string `json:"snippet"`
}

// ContextItem is one thing worth knowing, with what it rests on.
type ContextItem struct {
	RecordType datasource.EntityType `json:"record_type"`
	RecordID   ids.UUID              `json:"record_id"`
	Summary    string                `json:"summary"`
	Evidence   []ContextEvidence     `json:"evidence"`
}

// ContextSection groups items by what they are — recent activity, open tasks,
// related people. The names are the retriever's, not a closed set here.
type ContextSection struct {
	Name  string        `json:"name"`
	Items []ContextItem `json:"items"`
}

// AssembledContextResult is what catch_me_up_on answers, and what
// prep_for_meeting carries as its briefing.
type AssembledContextResult struct {
	Anchor   ContextAnchor    `json:"anchor"`
	Sections []ContextSection `json:"sections"`
}

// MeetingFocusItem is one open item pulled forward as something to raise.
type MeetingFocusItem struct {
	RecordID ids.UUID `json:"record_id"`
	Summary  string   `json:"summary"`
}

// PrepForMeetingResult is the assembled picture plus the focus list — the same
// briefing catch_me_up_on returns, so a caller can read either the same way.
type PrepForMeetingResult struct {
	Briefing     AssembledContextResult `json:"briefing"`
	MeetingFocus []MeetingFocusItem     `json:"meeting_focus"`
}

// QualifiedField is one gap the tool filled and the evidence it filled it from.
type QualifiedField struct {
	Value    string            `json:"value"`
	Evidence []ContextEvidence `json:"evidence"`
}

// QualifyLeadResult is what qualify_lead answers: what it could derive, and what
// it could not. The gaps are the honest half — they are what still needs a
// person, not a failure of the call.
type QualifyLeadResult struct {
	RecordID ids.UUID                  `json:"record_id"`
	Filled   map[string]QualifiedField `json:"filled"`
	Gaps     []string                  `json:"gaps"`
}

// ProgressDealResult is what progress_deal answers: the deal as it now stands,
// and the note it left if it left one.
type ProgressDealResult struct {
	Deal wireRecord `json:"deal"`
	// NoteActivityID is absent when the call carried no note. It is a pointer
	// rather than a zero uuid because "no note was asked for" and "a note whose
	// id is all zeroes" are different claims, and only one of them is true.
	NoteActivityID *ids.UUID `json:"note_activity_id,omitempty"`
}

// SlippingEvidence is one reason a deal is reported as slipping, named by the
// field it was read off.
type SlippingEvidence struct {
	Source  string `json:"source"`
	Snippet string `json:"snippet"`
}

// SlippingDealItem is one at-risk deal as the tool reports it.
type SlippingDealItem struct {
	Rank   int      `json:"rank"`
	DealID ids.UUID `json:"deal_id"`
	Name   string   `json:"name"`
	// AmountMinor and Currency are absent for a deal carrying no amount, which
	// is a real state — a deal can be worked before it is priced.
	AmountMinor *int64             `json:"amount_minor,omitempty"`
	Currency    *string            `json:"currency,omitempty"`
	Evidence    []SlippingEvidence `json:"evidence"`
}

// WhatsSlippingResult is what whats_slipping_this_week answers, worst first.
type WhatsSlippingResult struct {
	Deals []SlippingDealItem `json:"deals"`
}

// FollowUpDraft is one drafted follow-up, on the deal it was drafted for.
type FollowUpDraft struct {
	DealID          ids.UUID           `json:"deal_id"`
	DraftActivityID ids.UUID           `json:"draft_activity_id"`
	Summary         string             `json:"summary"`
	Evidence        []SlippingEvidence `json:"evidence"`
}

// DraftFollowUpsResult is what draft_follow_ups_for answers: which segment it
// worked over, and what it left on each deal's timeline.
type DraftFollowUpsResult struct {
	Segment string          `json:"segment"`
	Drafts  []FollowUpDraft `json:"drafts"`
}

// UpdateWithStagedApprovalResult is update_record's OTHER answer: the fields
// that applied, plus the ones a human last edited, which did not.
//
// It embeds the record rather than nesting it, because the applied half IS a
// record read-back and a caller reading the result should find the fields where
// every other tool puts them.
type UpdateWithStagedApprovalResult struct {
	wireRecord
	// A POINTER, and therefore optional, because this type declares BOTH shapes
	// update_record answers with: the plain read-back when every field applied,
	// and the read-back plus the staged note when some did not. Two schemas for
	// one tool would make a caller pick which it was reading; one schema with an
	// optional member says exactly what is true — the note is there when a human
	// last wrote one of the fields, and absent otherwise.
	StagedApproval *stagedApprovalNote `json:"staged_approval,omitempty"`
}

// SendEmailResult is what send_email answers once a human has released it.
type SendEmailResult struct {
	// ActivityID is the thread the sent message landed on — the same id
	// draft_email echoed, so a caller can follow the conversation.
	ActivityID ids.UUID `json:"activity_id"`
	// Status is what the delivery path accepted, not what the recipient did:
	// "accepted" means it left, never that it arrived.
	Status string `json:"status"`
}

// SendMessageResult is send_email's channel twin.
type SendMessageResult struct {
	ActivityID ids.UUID `json:"activity_id"`
	Status     string   `json:"status"`
}

// FreeSlot is one interval a host is free, as the scheduling store reports it.
type FreeSlot struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// AvailabilityResult is what check_availability answers.
//
// Truncated is not decoration: the walk stops at a cap, and a model handed a
// capped list with nothing marking it will tell a rep there is no later
// opening — the same failure AtRiskReport.Truncated exists to prevent.
type AvailabilityResult struct {
	Slots     []FreeSlot `json:"slots"`
	Truncated bool       `json:"truncated"`
}

// PassthroughEntityResult is the GUARANTEED SUBSET of a result whose handler
// answers with another module's whole contract entity — the booked meeting, the
// re-associated activity, the disqualified lead, the project in its new phase.
//
// It is a subset by construction and says so: this surface must not re-marshal
// those entities into a shape of its own, because that would drop whatever the
// entity carries and silently move the wire. So the schema states the one field
// every contract entity has and every caller needs, and — like every schema
// here — leaves additionalProperties open, which is exactly the claim "at least
// this". A caller that needs more reads the record it names.
type PassthroughEntityResult struct {
	ID ids.UUID `json:"id"`
}

// RunReportResult is the envelope every report answers with.
//
// Only the ROWS are dynamic. A report's columns come from the plan the caller
// sent, so `rows` is declared as objects and nothing is said about their
// members — but the envelope around them is the same for every report, and
// declaring it is what lets a caller find the columns, read the row count, and
// follow the drill-through handle without calling once to find out.
//
// The engine owns this shape; the members here are the ones its contract makes
// required, so this is a guaranteed subset like the passthroughs and the
// conformance suite holds it to a real report.
type RunReportResult struct {
	Report  string   `json:"report"`
	Columns []string `json:"columns"`
	// Plan is the validated query plan that ran — the caller's own request, back
	// in the words the engine accepted, so a model can see what its arguments
	// were understood to mean.
	Plan json.RawMessage `json:"plan"`
	// Rows are aggregate rows whose members ARE the columns above. Their shape
	// is the plan's, which is why nothing is declared about them here.
	Rows []json.RawMessage `json:"rows"`
	// TotalRows and DerivationURL are absent on a report that carries neither;
	// the handle is what "explain this number" follows.
	TotalRows     *int    `json:"total_rows,omitempty"`
	DerivationURL *string `json:"derivation_url,omitempty"`
	GeneratedAt   *string `json:"generated_at,omitempty"`
}

// marshalResult encodes a typed seam answer for the wire, carrying the seam's
// own failure through untouched.
//
// It exists so a handler over a typed seam reads as one line rather than four,
// and so the encode happens in ONE place: a seam answering with a value the
// encoder rejects is a defect in that seam, and there is a single spot where
// that is noticed rather than one per call site.
func marshalResult[T any](result T, err error) (json.RawMessage, error) {
	if err != nil {
		return nil, err
	}
	return json.Marshal(result)
}
